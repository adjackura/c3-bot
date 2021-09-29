package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type spec struct {
	Name        string
	Ingredients []variation
	Garnish     string
	// list of instructions
	Instructions []string
}

// list of ingredients in this variation
type variation []string

var (
	namePrefix         = "Name: "
	ingredientsPrefix  = "Ingredients:"
	garnishPrefix      = "Garnish: "
	instructionsPrefix = "Instructions:"
)

func (s *spec) String() string {
	var ingredients string
	var instructions string
	for i, v := range s.Ingredients {
		var newVar string
		for _, ing := range v {
			newVar = fmt.Sprintf("%s%s\n", newVar, strings.TrimSpace(ing))
		}
		if len(s.Ingredients) == 1 {
			ingredients = newVar
			break
		}
		newVar = fmt.Sprintf("*Variation %d:*\n%s\n", i+1, newVar)
		ingredients = fmt.Sprintf("%s%s", ingredients, newVar)
	}
	for _, i := range s.Instructions {
		instructions = fmt.Sprintf("%s%s\n", instructions, strings.TrimSpace(i))
	}
	ingredients = strings.TrimSpace(ingredients)
	return fmt.Sprintf(
		"%s%s\n\n%s\n%s\n\n%s%s\n\n%s\n%s", namePrefix, s.Name, ingredientsPrefix, ingredients, garnishPrefix, s.Garnish, instructionsPrefix, instructions)
}

func parseSpec(data []byte) (*spec, error) {
	var s spec
	return &s, json.Unmarshal(data, &s)
}
