package main

import (
	"log"

	"github.com/bwmarrin/discordgo"
)

func logInteractionError(s *discordgo.Session, i *discordgo.Interaction, err error) {
	s.FollowupMessageCreate(s.State.User.ID, i, true, &discordgo.WebhookParams{
		Content: "Something went wrong",
		Flags:   1 << 6,
	})
	log.Print(err)
}

func respond(s *discordgo.Session, i *discordgo.Interaction, content string, files []*discordgo.File, ephemeral bool) bool {
	flags := uint64(0)
	if ephemeral {
		flags = 1 << 6
	}
	if err := s.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   flags,
			Content: content,
			Files:   files,
		},
	}); err != nil {
		logInteractionError(s, i, err)
		return false
	}
	return true
}

func dm(s *discordgo.Session, id, content string) {
	// We create the private channel with the user who sent the message.
	channel, err := s.UserChannelCreate(id)
	if err != nil {
		log.Println("error creating channel:", err)
		return
	}
	// Then we send the message through the channel we created.
	_, err = s.ChannelMessageSend(channel.ID, content)
	if err != nil {
		log.Println("error sending dm:", err)
	}
}
