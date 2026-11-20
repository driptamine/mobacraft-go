// Package botai contains compiled data from GrimCraft's ai/ Lua source folder.
// Online decisions run in Go, inside the authoritative world simulation.
package botai

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed profiles.json
var compiled []byte

type Order struct {
	Kind string `json:"kind"`
	Type string `json:"type"`
}
type Profile struct {
	Capturers            int     `json:"capturers"`
	Engineers            int     `json:"engineers"`
	PointLimit           float64 `json:"pointLimit"`
	MinAttackSquads      int     `json:"minAttackSquads"`
	ArmyBudget           float64 `json:"armyBudget"`
	StopAttackRating     float64 `json:"stopAttackRating"`
	NoSecondaryBuildings bool    `json:"noSecondaryBuildings"`
	Opening              []Order `json:"opening"`
}
type Unit struct {
	Type    string  `json:"type"`
	Cost    float64 `json:"cost"`
	Size    int     `json:"size"`
	MaxSize int     `json:"maxSize"`
	HP      float64 `json:"hp"`
}
type Structure struct {
	Type      string  `json:"type"`
	Cost      float64 `json:"cost"`
	Generator bool    `json:"generator"`
}
type Faction struct {
	Units      []Unit      `json:"units"`
	Structures []Structure `json:"structures"`
	TechOrder  []string    `json:"techOrder"`
}
type Data struct {
	SourceHash    string                        `json:"sourceHash"`
	ThinkInterval float64                       `json:"thinkInterval"`
	Factions      map[string]Faction            `json:"factions"`
	Difficulties  map[string]map[string]Profile `json:"difficulties"`
}

var Settings Data

func ValidDifficulty(value string) bool { _, found := Settings.Difficulties[value]; return found }

func init() {
	if err := json.Unmarshal(compiled, &Settings); err != nil {
		panic(err)
	}
}
func For(faction, difficulty string) Profile {
	if profile, found := Settings.Difficulties[difficulty][faction]; found {
		return profile
	}
	return Settings.Difficulties["standard"][faction]
}
func IsGenerator(kind string) bool {
	return kind == "thermoGen" || strings.Contains(kind, "generator") || kind == "energy_core"
}

// IsListeningPost reports point-fortifying structures. Every race in the
// ai/ build data with one gets it from the computer player; Tau uses its
// own structure names, dark eldar has no listening post data at all.
func IsListeningPost(kind string) bool {
	return kind == "listening_post" || kind == "tau_listening_post" || kind == "tau_listening_post_sp"
}

// ListeningPostType follows the ai/ build data for a faction: the generic
// post when the race has one, the Tau post for Tau, "" when the race
// (dark eldar) has no listening post data.
func ListeningPostType(catalog Faction) string {
	for _, entry := range catalog.Structures {
		if entry.Type == "listening_post" {
			return entry.Type
		}
	}
	for _, entry := range catalog.Structures {
		if entry.Type == "tau_listening_post" {
			return entry.Type
		}
	}
	return ""
}
