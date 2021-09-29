package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"cloud.google.com/go/storage"
	"github.com/bwmarrin/discordgo"
	"google.golang.org/api/iterator"
)

var (
	token  = flag.String("token", "", "discord bot token")
	bucket = flag.String("bucket", "", "gcs bucket to use")

	cowman = "780258092042551376"

	waitingCreates    = waitingApproval{pending: map[string]*spec{}}
	waitingVariations = waitingApproval{pending: map[string]*spec{}}
)

var (
	commands = []*discordgo.ApplicationCommand{
		{
			Name:        "cocktail",
			Description: "Cowman's cocktail commands",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "random",
					Description: "display a random cocktail",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
				},
				{
					Name:        "search",
					Description: "search for a cocktail by name",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "name",
							Description: "name of the cocktail to search for",
							Required:    true,
						},
					},
				},
				{
					Name:        "list",
					Description: "list all cocktails",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
				},
				{
					Name:        "search-ingredients",
					Description: "search for cocktails by ingredients",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "ingredients",
							Description: "comma seperated list of ingredients to search by",
							Required:    true,
						},
					},
				},
			},
		},
		{
			Name:        "proposals",
			Description: "cocktail proposal commands",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "create",
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
					Name:        "create-variation",
					Description: "propose a variation on an existing spec",
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
							Description: "comma delineated list of ingredients for this new variation",
							Required:    true,
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
					Name:        "approve-variation",
					Description: "approve a proposed variation",
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
					Name:        "deny",
					Description: "deny a proposed spec",
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
					Name:        "deny-variation",
					Description: "deny a proposed variation",
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
					Name:        "list",
					Description: "list the current proposals",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
				},
				{
					Name:        "list-variations",
					Description: "list the current variation proposals",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
				},
			},
		},
	}
)

type waitingApproval struct {
	pending map[string]*spec
	sync.Mutex
}

func (a *waitingApproval) list() (ret []*spec) {
	a.Lock()
	defer a.Unlock()
	for _, v := range a.pending {
		ret = append(ret, v)
	}
	return
}

func (a *waitingApproval) get(k string) (*spec, bool) {
	a.Lock()
	defer a.Unlock()
	v, ok := a.pending[k]
	return v, ok
}

func (a *waitingApproval) add(k string, v *spec) {
	a.Lock()
	defer a.Unlock()
	a.pending[k] = v
}

func (a *waitingApproval) remove(k string) {
	a.Lock()
	defer a.Unlock()
	delete(a.pending, k)
}

func random(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	var files []*discordgo.File
	sp, pic, closer, err := randomCocktail(ctx, client)
	if err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}
	if pic != nil {
		defer closer()
		files = []*discordgo.File{
			pic,
		}
	}
	respond(s, i.Interaction, sp.String(), files, false)
}

func createCocktail(ctx context.Context, client *storage.Client, name string, data []byte) error {
	writer := client.Bucket(*bucket).Object(path.Join(name, "spec")).NewWriter(ctx)
	if _, err := io.Copy(writer, bytes.NewReader(data)); err != nil {
		return err
	}
	return writer.Close()
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

func getSpec(ctx context.Context, client *storage.Client, prefix string) (*spec, error) {
	reader, err := client.Bucket(*bucket).Object(path.Join(prefix, "spec")).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return parseSpec(data)
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

func getCocktail(ctx context.Context, client *storage.Client, cocktail string) (*spec, *discordgo.File, func() error, error) {
	sp, err := getSpec(ctx, client, cocktail)
	if err != nil {
		return nil, nil, nil, err
	}

	f, closer, err := randomPic(ctx, client, cocktail)
	return sp, f, closer, err
}

func randomCocktail(ctx context.Context, client *storage.Client) (*spec, *discordgo.File, func() error, error) {
	cocktails, err := listCocktails(ctx, client)
	if err != nil {
		return nil, nil, nil, err
	}
	rand.Seed(time.Now().UnixNano())
	prefix := cocktails[rand.Intn(len(cocktails))]
	return getCocktail(ctx, client, prefix)
}

func normalizeName(name string) string {
	return strings.ReplaceAll(strings.TrimSpace(strings.ToLower(name)), " ", "-")
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
