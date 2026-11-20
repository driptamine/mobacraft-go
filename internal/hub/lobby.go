package hub

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"grimcraft-go/internal/balance"
	"grimcraft-go/internal/botai"
	"grimcraft-go/internal/sim"
	"time"
)

func (gameHub *Hub) BroadcastLobby(lobbyID string, exclude ...int) {
	gameHub.mutex.Lock()
	lobby := gameHub.lobbies[lobbyID]
	if lobby == nil {
		gameHub.mutex.Unlock()
		return
	}
	data, _ := json.Marshal(map[string]any{"Lobby": lobby})
	room := lobby.room
	gameHub.mutex.Unlock()
	for _, client := range room.Clients() {
		if len(exclude) > 0 && client.PlayerID == exclude[0] {
			continue
		}
		select {
		case client.Out <- data:
		default:
		}
	}
}

// HandleLobbyCommand is shared by WebSocket and WebTransport clients.
func (gameHub *Hub) HandleLobbyCommand(lobbyID string, client *Client, command map[string]any) (handled, isValid bool, reason string) {
	_, start := command["StartGame"]
	_, loaded := command["PlayerLoaded"]
	factionArgs, factionChange := command["SetLobbyFaction"]
	heroArgs, heroChange := command["SetLobbyHero"]
	if !start && !factionChange && !loaded && !heroChange {
		return false, false, ""
	}
	gameHub.mutex.Lock()
	lobby := gameHub.lobbies[lobbyID]
	if lobby == nil {
		gameHub.mutex.Unlock()
		return true, false, "unknown lobby"
	}
	if loaded {
		if !lobby.Started {
			gameHub.mutex.Unlock()
			return true, false, "match has not started loading"
		}
		room := lobby.room
		room.mutex.Lock()
		if room.clients[client.PlayerID] != client {
			room.mutex.Unlock()
			gameHub.mutex.Unlock()
			return true, false, "not a member"
		}
		room.loaded[client.PlayerID] = true
		room.maybeStartLocked()
		if room.dota && !room.running {
			room.broadcastSnapshotLocked(true)
		}
		room.mutex.Unlock()
		gameHub.mutex.Unlock()
		return true, true, ""
	}
	dota := sim.IsDotaMap(lobby.Map)
	if heroChange {
		if !dota || lobby.Started {
			gameHub.mutex.Unlock()
			return true, false, "hero selection is unavailable"
		}
		arguments, _ := heroArgs.(map[string]any)
		hero, _ := arguments["hero"].(string)
		if !sim.ValidDotaHero(hero) {
			gameHub.mutex.Unlock()
			return true, false, "unknown hero"
		}
		for index := range lobby.Players {
			if lobby.Players[index].PlayerID == client.PlayerID {
				lobby.Players[index].Hero = hero
				gameHub.mutex.Unlock()
				gameHub.BroadcastLobby(lobbyID)
				return true, true, ""
			}
		}
		gameHub.mutex.Unlock()
		return true, false, "not a member"
	}
	if factionChange {
		if dota {
			gameHub.mutex.Unlock()
			return true, false, "choose a hero in this lobby"
		}
		if lobby.Started {
			gameHub.mutex.Unlock()
			return true, false, "game already started"
		}
		arguments, _ := factionArgs.(map[string]any)
		faction, _ := arguments["faction"].(string)
		client.Faction = faction
		for index := range lobby.Players {
			if lobby.Players[index].PlayerID == client.PlayerID {
				lobby.Players[index].Faction = faction
			}
		}
		lobby.room.world.SetFaction(client.PlayerID, faction)
		gameHub.mutex.Unlock()
		gameHub.BroadcastLobby(lobbyID)
		return true, true, ""
	}
	if client.PlayerID != lobby.HostID {
		gameHub.mutex.Unlock()
		return true, false, "only the host can start"
	}
	if lobby.Started {
		gameHub.mutex.Unlock()
		return true, true, ""
	}
	var currentArguments struct {
		BalanceMod *balance.Mod `json:"balanceMod"`
		Computers  []struct {
			Faction    string `json:"faction"`
			Hero       string `json:"hero"`
			Difficulty string `json:"difficulty"`
			Team       int    `json:"team"`
		} `json:"computers"`
	}
	encodedData, _ := json.Marshal(command["StartGame"])
	if operationError := json.Unmarshal(encodedData, &currentArguments); operationError != nil {
		gameHub.mutex.Unlock()
		return true, false, "invalid computer slots"
	}
	if len(lobby.Players)+len(currentArguments.Computers) > lobby.Total {
		gameHub.mutex.Unlock()
		return true, false, "lobby full"
	}
	teams := map[int]bool{}
	for _, playerSlot := range lobby.Players {
		teams[playerSlot.Team] = true
	}
	for _, bot := range currentArguments.Computers {
		if bot.Difficulty != "" && !botai.ValidDifficulty(bot.Difficulty) {
			gameHub.mutex.Unlock()
			return true, false, "invalid AI difficulty"
		}
		if bot.Team != 1 && bot.Team != 2 {
			gameHub.mutex.Unlock()
			return true, false, "invalid computer team"
		}
		if dota && bot.Hero != "" && !sim.ValidDotaHero(bot.Hero) {
			gameHub.mutex.Unlock()
			return true, false, "unknown computer hero"
		}
		teams[bot.Team] = true
	}
	if len(teams) < 2 {
		gameHub.mutex.Unlock()
		return true, false, "add an opponent or wait for another player"
	}
	if !dota && currentArguments.BalanceMod != nil {
		if operationError := lobby.room.world.InstallBalance(currentArguments.BalanceMod); operationError != nil {
			gameHub.mutex.Unlock()
			return true, false, "invalid balance mod: " + operationError.Error()
		}
		encodedMod, _ := json.Marshal(currentArguments.BalanceMod)
		lobby.BalanceMod = currentArguments.BalanceMod
		lobby.BalanceHash = fmt.Sprintf("%x", sha256.Sum256(encodedMod))
	}
	for currentIndex, bot := range currentArguments.Computers {
		team := "Blue"
		if bot.Team == 2 {
			team = "Red"
		}
		owner := -(currentIndex + 1)
		hero := bot.Hero
		if dota {
			bot.Faction = "ig"
			if hero == "" {
				hero = sim.DefaultDotaHero(team)
			}
		} else {
			lobby.room.world.AddComputer(team, owner, bot.Faction, lobby.Map)
			lobby.room.world.SetComputerDifficulty(owner, bot.Difficulty)
		}
		lobby.Players = append(lobby.Players, PlayerSlot{PlayerID: owner, Controller: "computer", Difficulty: bot.Difficulty, Username: fmt.Sprintf("Computer %d", currentIndex+1), Faction: bot.Faction, Hero: hero, Team: bot.Team})
	}
	lobby.room.mutex.Lock()
	if dota {
		seats := make([]sim.DotaSeat, 0, len(lobby.Players))
		for _, slot := range lobby.Players {
			team := "Blue"
			if slot.Team == 2 {
				team = "Red"
			}
			seats = append(seats, sim.DotaSeat{Owner: slot.PlayerID, Team: team, Hero: slot.Hero, Computer: slot.Controller == "computer"})
		}
		lobby.room.world.StartDotaMatch(seats)
	} else {
		lobby.room.world.ResetMatchClock()
	}
	lobby.room.loading = true
	lobby.room.running = false
	lobby.room.loaded = map[int]bool{}
	lobby.room.startsAt = time.Time{}
	if dota {
		lobby.room.broadcastSnapshotLocked(false)
	}
	lobby.room.mutex.Unlock()
	lobby.Started = true
	lobby.Current = len(lobby.Players)
	gameHub.mutex.Unlock()
	gameHub.BroadcastLobby(lobbyID)
	return true, true, ""
}
