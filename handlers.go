package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/bwmarrin/discordgo"
)

func list(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	cocktails, err := listCocktails(ctx, client)
	if err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}

	var content string
	for _, cocktail := range cocktails {
		content = fmt.Sprintf("%s    %s\n", content, cocktail)
	}
	respond(s, i.Interaction, fmt.Sprintf("I currently know about %d cocktails:\n%s", len(cocktails), content), nil, true)
}

func search(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	name := i.ApplicationCommandData().Options[0].Options[0].StringValue()
	cocktails, err := listCocktails(ctx, client)
	if err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}

	for _, cocktail := range cocktails {
		if normalizeName(cocktail) == normalizeName(name) {
			var files []*discordgo.File
			sp, pic, closer, err := getCocktail(ctx, client, cocktail)
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
			return
		}
	}
	// If we got here that means that the name is not an exact match, try doing prefix matches.
	var matches []string
	for _, cocktail := range cocktails {
		if strings.HasPrefix(normalizeName(cocktail), normalizeName(name)) {
			matches = append(matches, cocktail)
		}
	}

	// No matches
	if len(matches) == 0 {
		respond(s, i.Interaction, fmt.Sprintf("No matches, for %q", name), nil, true)
		return
	}

	// One match
	if len(matches) == 1 {
		var files []*discordgo.File
		sp, pic, closer, err := getCocktail(ctx, client, matches[0])
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
		return
	}

	// Multiple matches
	var content string
	for _, match := range matches {
		content = fmt.Sprintf("%s%s\n", content, match)
	}
	respond(s, i.Interaction, "Multiple matches:\n"+content, nil, true)
}

func searchIngredients(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: 1 << 6,
		},
	}); err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}

	ingredients := strings.Split(i.ApplicationCommandData().Options[0].Options[0].StringValue(), ",")
	cocktails, err := listCocktails(ctx, client)
	if err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}
	var fullMatches []string
	var partialMatches []string
	for _, cocktail := range cocktails {
		sp, err := getSpec(ctx, client, cocktail)
		if err != nil {
			logInteractionError(s, i.Interaction, err)
			continue
		}
		var matches int
		for _, wantI := range ingredients {
			wantI = strings.ToLower(wantI)
			for _, v := range sp.Ingredients {
				for _, ci := range v {
					ci = strings.ToLower(ci)
					if strings.Contains(ci, wantI) {
						matches++
						break
					}
				}
			}
		}
		if matches == len(ingredients) {
			fullMatches = append(fullMatches, cocktail)
		} else if matches > 0 {
			partialMatches = append(partialMatches, cocktail)
		}
	}

	if len(fullMatches) == 0 && len(partialMatches) == 0 {
		content := fmt.Sprintf("Seach for cocktails containing %q resulted in no matches", ingredients)
		if _, err := s.InteractionResponseEdit(s.State.User.ID, i.Interaction, &discordgo.WebhookEdit{
			Content: content,
		}); err != nil {
			logInteractionError(s, i.Interaction, err)
		}
		return
	}

	content := fmt.Sprintf("Seach for cocktails containing %q resulted in %d full matches and %d partial matches:", ingredients, len(fullMatches), len(partialMatches))
	if len(fullMatches) > 0 {
		content = fmt.Sprintf("%s**%d full matches:**\n", content, len(fullMatches))
		for _, c := range fullMatches {
			content = fmt.Sprintf("%s    %s\n", content, c)
		}
	}
	if len(partialMatches) > 0 {
		content = fmt.Sprintf("%s**%d partial matches:**\n", content, len(partialMatches))
		for _, c := range partialMatches {
			content = fmt.Sprintf("%s    %s\n", content, c)
		}
	}
	if _, err := s.InteractionResponseEdit(s.State.User.ID, i.Interaction, &discordgo.WebhookEdit{
		Content: content,
	}); err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}
}

func createProposal(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
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
		Name:         name.StringValue(),
		Ingredients:  []variation{strings.Split(ingredients.StringValue(), ",")},
		Instructions: strings.Split(instructions.StringValue(), ","),
		Garnish:      g,
	}

	cocktails, err := listCocktails(ctx, client)
	if err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}

	for _, cocktail := range cocktails {
		if normalizeName(cocktail) == normalizeName(name.StringValue()) {
			respond(s, i.Interaction, fmt.Sprintf("%s already exists, maybe try adding a variation?", cocktail), nil, true)
			return
		}
	}

	content := fmt.Sprintf("Spec waiting on approval, you can edit by running create again:\n%s", sp)
	if !respond(s, i.Interaction, content, nil, true) {
		return
	}
	waitingCreates.add(normalizeName(name.StringValue()), sp)

	// DM Cowman
	var user *discordgo.User
	if i.Member != nil {
		user = i.Member.User
	} else {
		user = i.User
	}
	guildName := "DM"
	guild, err := s.Guild(i.GuildID)
	if err == nil {
		guildName = guild.Name
	}
	content = fmt.Sprintf("Spec submitted by %q in %q:\n%s", user.Username, guildName, sp)
	dm(s, cowman, content)
}

func createVariation(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
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

	cocktails, err := listCocktails(ctx, client)
	if err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}

	var found string
	for _, cocktail := range cocktails {
		if normalizeName(cocktail) == normalizeName(name.StringValue()) {
			found = cocktail
		}
	}
	if found == "" {
		respond(s, i.Interaction, fmt.Sprintf("%s not found, can't propose variation", name.StringValue()), nil, true)
	}

	sp := &spec{
		Name:        found,
		Ingredients: []variation{strings.Split(ingredients.StringValue(), ",")},
	}

	content := fmt.Sprintf("Variation waiting on approval, you can edit by running 'create-variation' again:\n%s", sp)
	if !respond(s, i.Interaction, content, nil, true) {
		return
	}
	waitingVariations.add(normalizeName(name.StringValue()), sp)

	// DM Cowman
	var user *discordgo.User
	if i.Member != nil {
		user = i.Member.User
	} else {
		user = i.User
	}
	guildName := "DM"
	guild, err := s.Guild(i.GuildID)
	if err == nil {
		guildName = guild.Name
	}
	content = fmt.Sprintf("Variation submitted by %q in %q:\n%s", user.Username, guildName, sp)
	dm(s, cowman, content)
}

func approveProposal(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	var user *discordgo.User
	if i.Member != nil {
		user = i.Member.User
	} else {
		user = i.User
	}
	if user.ID != cowman {
		respond(s, i.Interaction, "You're not my boss!", nil, true)
		return
	}

	name := i.ApplicationCommandData().Options[0].Options[0].StringValue()
	sp, ok := waitingCreates.get(normalizeName(name))
	if !ok {
		respond(s, i.Interaction, fmt.Sprintf("%q not found", name), nil, true)
		return
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: 1 << 6,
		},
	}); err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}

	data, err := json.Marshal(sp)
	if err != nil {
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
	waitingCreates.remove(normalizeName(name))
}

func approveVariation(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	var user *discordgo.User
	if i.Member != nil {
		user = i.Member.User
	} else {
		user = i.User
	}
	if user.ID != cowman {
		respond(s, i.Interaction, "You're not my boss!", nil, true)
		return
	}

	name := i.ApplicationCommandData().Options[0].Options[0].StringValue()
	v, ok := waitingVariations.get(normalizeName(name))
	if !ok {
		respond(s, i.Interaction, fmt.Sprintf("%q not found", name), nil, true)
		return
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: 1 << 6,
		},
	}); err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}

	cur, err := getSpec(ctx, client, v.Name)
	if err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}

	// We just take the ingredients from the proposed variation.
	cur.Ingredients = append(cur.Ingredients, v.Ingredients...)

	data, err := json.Marshal(cur)
	if err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}
	if err := createCocktail(ctx, client, cur.Name, data); err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}
	if _, err := s.InteractionResponseEdit(s.State.User.ID, i.Interaction, &discordgo.WebhookEdit{
		Content: fmt.Sprintf("%q approved and updated.", cur.Name),
	}); err != nil {
		logInteractionError(s, i.Interaction, err)
		return
	}
	waitingCreates.remove(normalizeName(name))
}

func denyProposal(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	var user *discordgo.User
	if i.Member != nil {
		user = i.Member.User
	} else {
		user = i.User
	}
	if user.ID != cowman {
		respond(s, i.Interaction, "You're not my boss!", nil, true)
		return
	}

	name := i.ApplicationCommandData().Options[0].Options[0].StringValue()
	_, ok := waitingCreates.get(normalizeName(name))
	if !ok {
		respond(s, i.Interaction, fmt.Sprintf("%q not found", name), nil, true)
		return
	}
	waitingCreates.remove(normalizeName(name))
	respond(s, i.Interaction, fmt.Sprintf("%q denied", name), nil, true)
}

func denyVariation(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	var user *discordgo.User
	if i.Member != nil {
		user = i.Member.User
	} else {
		user = i.User
	}
	if user.ID != cowman {
		respond(s, i.Interaction, "You're not my boss!", nil, true)
		return
	}

	name := i.ApplicationCommandData().Options[0].Options[0].StringValue()
	_, ok := waitingVariations.get(normalizeName(name))
	if !ok {
		respond(s, i.Interaction, fmt.Sprintf("%q not found", name), nil, true)
		return
	}
	waitingVariations.remove(normalizeName(name))
	respond(s, i.Interaction, fmt.Sprintf("%q denied", name), nil, true)
}

func listProposals(s *discordgo.Session, i *discordgo.InteractionCreate) {
	waitingList := waitingCreates.list()
	content := fmt.Sprintf("%d proposals pending\n\n", len(waitingList))
	for _, p := range waitingList {
		content = fmt.Sprintf("%s%s\n\n", content, p)
	}

	respond(s, i.Interaction, content, nil, true)
}

func listVariations(s *discordgo.Session, i *discordgo.InteractionCreate) {
	waitingList := waitingVariations.list()
	content := fmt.Sprintf("%d variations pending\n\n", len(waitingList))
	for _, p := range waitingList {
		content = fmt.Sprintf("%s%s\n\n", content, p)
	}

	respond(s, i.Interaction, content, nil, true)
}

func baseHandler(ctx context.Context, client *storage.Client, s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.ApplicationCommandData().Name {
	case "cocktail":
		switch i.ApplicationCommandData().Options[0].Name {
		case "random":
			random(ctx, client, s, i)
		case "search":
			search(ctx, client, s, i)
		case "search-ingredients":
			searchIngredients(ctx, client, s, i)
		case "list":
			list(ctx, client, s, i)
		}
	case "proposals":
		switch i.ApplicationCommandData().Options[0].Name {
		case "create":
			createProposal(ctx, client, s, i)
		case "create-variation":
			createVariation(ctx, client, s, i)
		case "list":
			listProposals(s, i)
		case "list-variations":
			listVariations(s, i)
		case "deny":
			denyProposal(ctx, client, s, i)
		case "deny-variation":
			denyVariation(ctx, client, s, i)
		case "approve":
			approveProposal(ctx, client, s, i)
		case "approve-variation":
			approveVariation(ctx, client, s, i)
		}
	}
}

// messageCreate is required because commands don't support files yet.
func messageCreate(ctx context.Context, client *storage.Client, s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	prefix := "/c3 upload-picture"
	if !strings.HasPrefix(m.Message.Content, prefix) {
		return
	}

	if m.Author.ID != cowman {
		return
	}

	name := strings.TrimPrefix(m.Message.Content, prefix)
	cocktails, err := listCocktails(ctx, client)
	if err != nil {
		fmt.Println(err)
		return
	}
	var found string
	for _, cocktail := range cocktails {
		if normalizeName(strings.TrimSuffix(cocktail, "/")) == normalizeName(name) {
			found = cocktail
			break
		}
	}
	if found == "" {
		if _, err := s.ChannelMessageSend(m.ChannelID, "Cocktail not found: "+name); err != nil {
			log.Print(err)
		}
		return
	}

	for _, attach := range m.Attachments {
		writer := client.Bucket(*bucket).Object(path.Join(found, "pictures", attach.Filename)).NewWriter(ctx)
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
