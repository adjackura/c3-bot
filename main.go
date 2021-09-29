package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/storage"
	"github.com/bwmarrin/discordgo"
	"google.golang.org/api/iterator"
)

var (
	token  = flag.String("token", "", "discord bot token")
	bucket = flag.String("bucket", "", "gcs bucket to use")

	approvedUser = "780258092042551376"
)

var (
	commands = []*discordgo.ApplicationCommand{
		{
			Name:        "c3",
			Description: "Cowman's cocktail commands",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "random",
					Description: "random cocktail",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
				},
				{
					Name:        "search",
					Description: "show a random picture for this cocktail",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "name",
							Description: "name of the cocktail to show, mutually exclusive with other options",
							Required:    false,
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "ingredients",
							Description: "comma seperated list of ingredients to search by",
							Required:    false,
						},
					},
				},
				{
					Name:        "propose",
					Description: "propose a new spec",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "name",
							Description: "name of the cocktail",
							Required:    true,
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "ingredients",
							Description: "comma delineated list of ingredients",
							Required:    true,
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "instructions",
							Description: "comma delineated list of ingredients",
							Required:    true,
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "garnish",
							Description: "garnish",
							Required:    false,
						},
					},
				},
				{
					Name:        "approve",
					Description: "approve a proposed spec",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "name",
							Description: "name of the proposal",
							Required:    true,
						},
					},
				},
				{
					Name:        "list-proposals",
					Description: "list the current proposals",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
				},
			},
		},
	}
)

var waitingApproval = map[string]string{}

func logInteractionError(s *discordgo.Session, i *discordgo.Interaction, err error) {
	s.FollowupMessageCreate(s.State.User.ID, i, true, &discordgo.WebhookParams{
		Content: "Something went wrong",
	})
	log.Print(err)
}

func respond(s *discordgo.Session, i *discordgo.Interaction, content string, files []*discordgo.File) bool {
	if err := s.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Files:   files,
		},
	}); err != nil {
		logInteractionError(s, i, err)
		return false
	}
	return true
}

func random(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	var files []*discordgo.File
	spec, pic, closer, err := randomCocktail(ctx, client)
	if err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}
	content := string(spec)
	if pic != nil {
		defer closer()
		files = []*discordgo.File{
			pic,
		}
	}
	respond(s, i.Interaction, content, files)
}

func search(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	if len(i.ApplicationCommandData().Options[0].Options) == 0 {
		respond(s, i.Interaction, "Must provide a search option.", nil)
		return
	}

	var name *discordgo.ApplicationCommandInteractionDataOption
	var ingredients *discordgo.ApplicationCommandInteractionDataOption
	for _, opt := range i.ApplicationCommandData().Options[0].Options {
		switch opt.Name {
		case "name":
			name = opt
		case "ingredients":
			ingredients = opt
		}
	}
	if name != nil && ingredients != nil {
		respond(s, i.Interaction, "Must provide only one search option.", nil)
		return
	}

	if name != nil {
		cocktails, err := listCocktails(ctx, client)
		if err != nil {
			logInteractionError(s, i.Interaction, err)
			return
		}

		for _, cocktail := range cocktails {
			if cocktail == normalizeName(name.StringValue()) {
				var files []*discordgo.File
				spec, pic, closer, err := getCocktail(ctx, client, cocktail)
				if err != nil {
					logInteractionError(s, i.Interaction, err)
					return
				}
				content := string(spec)
				if pic != nil {
					defer closer()
					files = []*discordgo.File{
						pic,
					}
				}
				respond(s, i.Interaction, content, files)
				return
			}
		}
		// If we got here that means that the name is not an exact match, try doing prefix matches.
		var matches string
		for _, cocktail := range cocktails {
			if strings.HasPrefix(cocktail, normalizeName(name.StringValue())) {
				matches = fmt.Sprintf("%s%s\n", matches, cocktail)
			}
		}

		if matches == "" {
			respond(s, i.Interaction, fmt.Sprintf("No matches, for %q", normalizeName(name.StringValue())), nil)
			return
		}
		respond(s, i.Interaction, "No exact matches, here are some partial matches:\n"+matches, nil)
		return
	}

	if ingredients != nil {
		content := "I don't know how to do this yet"
		if len(i.ApplicationCommandData().Options[0].Options) >= 1 {
			content = fmt.Sprintf("%s with ingredients %q", content, strings.Split(ingredients.StringValue(), ","))
		}
		respond(s, i.Interaction, content, nil)
	}
}

func propose(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var name *discordgo.ApplicationCommandInteractionDataOption
	var ingredients *discordgo.ApplicationCommandInteractionDataOption
	var instructions *discordgo.ApplicationCommandInteractionDataOption
	var garnish *discordgo.ApplicationCommandInteractionDataOption
	for _, opt := range i.ApplicationCommandData().Options[0].Options {
		switch opt.Name {
		case "name":
			name = opt
		case "ingredients":
			ingredients = opt
		case "instructions":
			instructions = opt
		case "garnish":
			garnish = opt
		}
	}

	g := "None"
	if garnish != nil {
		g = garnish.StringValue()
	}
	sp := &spec{
		name:         name.StringValue(),
		ingredients:  strings.Split(ingredients.StringValue(), ","),
		instructions: strings.Split(instructions.StringValue(), ","),
		garnish:      g,
	}
	out := sp.marshal()
	content := fmt.Sprintf("Spec waiting on approval, you can edit by running create again:\n%s", out)
	if !respond(s, i.Interaction, content, nil) {
		return
	}
	waitingApproval[name.StringValue()] = out
}

func approve(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Member != nil {
		if i.Member.User.ID != approvedUser {
			respond(s, i.Interaction, "You're not my boss!", nil)
		}
	}
	if i.User != nil {
		if i.User.ID != approvedUser {
			respond(s, i.Interaction, "You're not my boss!", nil)
		}
	}
	name := i.ApplicationCommandData().Options[0].Options[0].StringValue()
	data, ok := waitingApproval[name]
	if !ok {
		if !respond(s, i.Interaction, fmt.Sprintf("%q not found", name), nil) {
			return
		}
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}
	if err := createCocktail(ctx, client, name, data); err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}
	if _, err := s.InteractionResponseEdit(s.State.User.ID, i.Interaction, &discordgo.WebhookEdit{
		Content: fmt.Sprintf("%q approved and uploaded.", name),
	}); err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}
	delete(waitingApproval, name)
}

func listProposals(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Member != nil {
		if i.Member.User.ID != approvedUser {
			respond(s, i.Interaction, "You're not my boss!", nil)
		}
	}
	if i.User != nil {
		if i.User.ID != approvedUser {
			respond(s, i.Interaction, "You're not my boss!", nil)
		}
	}
	content := fmt.Sprintf("%d proposals\n\n", len(waitingApproval))
	for _, p := range waitingApproval {
		content = fmt.Sprintf("%s%s\n\n", content, p)
	}

	if !respond(s, i.Interaction, content, nil) {
		return
	}
}

func baseHandler(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.ApplicationCommandData().Options[0].Name {
	case "random":
		random(ctx, client, s, i)
	case "search":
		search(ctx, client, s, i)
	case "propose":
		propose(s, i)
	case "approve":
		approve(ctx, client, s, i)
	case "list-proposals":
		listProposals(s, i)
	}
}

func createCocktail(ctx context.Context, client *storage.Client, name, data string) error {
	writer := client.Bucket(*bucket).Object(path.Join(normalizeName(name), "spec")).NewWriter(ctx)
	if _, err := io.Copy(writer, strings.NewReader(data)); err != nil {
		return err
	}
	return writer.Close()
}

type spec struct {
	name         string
	ingredients  []string
	garnish      string
	instructions []string
}

var (
	namePrefix         = "Name: "
	ingredientsPrefix  = "Ingredients:"
	garnishPrefix      = "Garnish: "
	instructionsPrefix = "Instructions:"
)

func (s *spec) marshal() string {
	var ingredients string
	var instructions string
	for _, i := range s.ingredients {
		ingredients = fmt.Sprintf("%s%s\n", ingredients, strings.TrimSpace(i))
	}
	for _, i := range s.instructions {
		instructions = fmt.Sprintf("%s%s\n", instructions, strings.TrimSpace(i))
	}
	ingredients = strings.TrimSpace(ingredients)
	return fmt.Sprintf(
		"%s%s\n\n%s\n%s\n\n%s%s\n\n%s\n%s", namePrefix, s.name, ingredientsPrefix, ingredients, garnishPrefix, s.garnish, instructionsPrefix, instructions)
}

func parseSpec(raw []byte) spec {
	var s spec
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	var ingredients bool
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			ingredients = false
			continue
		case strings.HasPrefix(line, namePrefix):
			ingredients = false
			s.name = strings.TrimPrefix(line, namePrefix)
		case strings.HasPrefix(line, ingredientsPrefix):
			ingredients = true
		default:
			if ingredients {
				s.ingredients = append(s.ingredients, line)
			}
		}
	}
	return s
}

func listCocktails(ctx context.Context, client *storage.Client) ([]string, error) {
	bkt := client.Bucket(*bucket)
	query := &storage.Query{Delimiter: "/"}
	query.SetAttrSelection([]string{"Prefix"})

	var cocktails []string
	it := bkt.Objects(ctx, query)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		cocktails = append(cocktails, strings.TrimSuffix(attrs.Prefix, "/"))
	}
	return cocktails, nil
}

func getSpec(ctx context.Context, client *storage.Client, prefix string) ([]byte, error) {
	reader, err := client.Bucket(*bucket).Object(path.Join(prefix, "spec")).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return ioutil.ReadAll(reader)
}

func randomPic(ctx context.Context, client *storage.Client, prefix string) (*discordgo.File, func() error, error) {
	bkt := client.Bucket(*bucket)
	prefix = path.Join(prefix, "pictures")
	query := &storage.Query{Prefix: prefix}
	query.SetAttrSelection([]string{"Name"})

	var pics []string
	it := bkt.Objects(ctx, query)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if attrs.Name == prefix+"/" {
			continue
		}
		pics = append(pics, attrs.Name)
	}
	if len(pics) == 0 {
		log.Printf("No pictures for %q", prefix)
		return nil, nil, nil
	}
	name := pics[rand.Intn(len(pics))]
	fmt.Println("pic name:", name)

	attrs, err := bkt.Object(name).Attrs(ctx)
	if err != nil {
		return nil, nil, err
	}

	var sFile discordgo.File
	sFile.ContentType = attrs.ContentType
	sFile.Name = path.Base(attrs.Name)

	reader, err := bkt.Object(name).NewReader(ctx)
	if err != nil {
		return nil, nil, err
	}
	sFile.Reader = reader
	return &sFile, reader.Close, nil
}

func getCocktail(ctx context.Context, client *storage.Client, cocktail string) ([]byte, *discordgo.File, func() error, error) {
	data, err := getSpec(ctx, client, cocktail)
	if err != nil {
		return nil, nil, nil, err
	}

	f, closer, err := randomPic(ctx, client, cocktail)
	return data, f, closer, err
}

func randomCocktail(ctx context.Context, client *storage.Client) ([]byte, *discordgo.File, func() error, error) {
	cocktails, err := listCocktails(ctx, client)
	if err != nil {
		return nil, nil, nil, err
	}
	rand.Seed(time.Now().UnixNano())
	prefix := cocktails[rand.Intn(len(cocktails))]
	fmt.Println("prefix:", prefix)
	return getCocktail(ctx, client, prefix)
}

func main() {
	ctx := context.Background()
	flag.Parse()

	gcsClient, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}

	s, err := discordgo.New("Bot " + *token)
	if err != nil {
		log.Fatalf("Invalid bot parameters: %v", err)
	}

	// Start handlers.
	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		baseHandler(ctx, gcsClient, s, i)
	})
	s.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		messageCreate(ctx, gcsClient, s, m)
	})
	s.Identify.Intents = discordgo.IntentsGuildMessages

	// Open a websocket connection to Discord and begin listening.
	if err := s.Open(); err != nil {
		log.Fatalf("Error opening connection: %v", err)
	}
	defer s.Close()

	// Create commands on server
	if _, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, "", commands); err != nil {
		log.Fatalf("Cannot create commands: %v", err)
	}

	// Wait here until CTRL-C or other term signal is received.
	fmt.Println("Bot is now running.  Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	<-sc
}

func normalizeName(name string) string {
	return strings.ReplaceAll(strings.TrimSpace(strings.ToLower(name)), " ", "-")
}

func messageCreate(ctx context.Context, client *storage.Client, s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}
	fmt.Println(m.Content)

	if !strings.HasPrefix(m.Message.Content, "/c3-cocktail upload-picture") {
		return
	}
	name := strings.TrimPrefix(m.Message.Content, "/c3-cocktail upload-picture")
	name = normalizeName(name)
	cocktails, err := listCocktails(ctx, client)
	if err != nil {
		fmt.Println(err)
		return
	}
	var found bool
	for _, cocktail := range cocktails {
		if strings.TrimSuffix(cocktail, "/") == name {
			found = true
			break
		}
	}
	if !found {
		if _, err := s.ChannelMessageSend(m.ChannelID, "Cocktail not found: "+name); err != nil {
			log.Print(err)
		}
		return
	}

	for _, attach := range m.Attachments {
		writer := client.Bucket(*bucket).Object(path.Join(name, "pictures", attach.Filename)).NewWriter(ctx)
		resp, err := http.Get(attach.URL)
		if err != nil {
			fmt.Println(err)
			continue
		}
		defer resp.Body.Close()
		if _, err := io.Copy(writer, resp.Body); err != nil {
			fmt.Println(err)
			continue
		}
		if err := writer.Close(); err != nil {
			fmt.Println(err)
			continue
		}
	}
	if _, err := s.ChannelMessageSend(m.ChannelID, "Attachments uploaded for "+name); err != nil {
		log.Print(err)
	}
}
