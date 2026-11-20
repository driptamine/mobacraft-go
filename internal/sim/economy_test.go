package sim

import (
	"grimcraft-go/internal/protocol"
	"math"
	"testing"
)

func TestStartingEconomyAndBaseIncome(test *testing.T) {
	for _, faction := range []string{"ig", "chaos", "space marines", "orks", "eldar", "dark eldar", "tau", "sob", "necrons"} {
		test.Run(faction, func(test *testing.T) {
			world := NewWorld()
			world.SpawnLobbySeat("Blue", 1, faction, "Arena")
			world.SpawnLobbySeat("Red", 2, "ig", "Arena")
			expectedStart, expectedIncome := 1000.0, 20.0
			if faction == "necrons" {
				expectedStart, expectedIncome = 1500, 10
			}
			player := world.Players[1]
			if player.Requisition != expectedStart || player.Energy != 100 {
				test.Fatalf("bad starting resources: %+v", player)
			}
			for tick := 0; tick < 100; tick++ {
				world.Advance(.1)
			}
			if math.Abs(player.Requisition-(expectedStart+expectedIncome)) > .0001 || player.Energy != 100 {
				test.Fatalf("bad ten-second income: %+v", player)
			}
		})
	}
}
func TestListeningPostIncomeAndSnapshotRate(test *testing.T) {
	for tier, pointRate := range []float64{6, 12, 18, 24} {
		test.Run(string(rune('0'+tier)), func(test *testing.T) {
			world := NewWorld()
			world.SpawnLobbySeat("Blue", 1, "ig", "Arena")
			world.SpawnLobbySeat("Red", 2, "ig", "Arena")
			for _, point := range world.Points {
				point.Owner = "Blue"
				point.LPTier = tier
				point.HasListeningPost = tier > 0
				break
			}
			for tick := 0; tick < 100; tick++ {
				world.Advance(.1)
			}
			player := world.Players[1]
			if math.Abs(player.Requisition-(1000+20+pointRate)) > .0001 {
				test.Fatalf("LP%d income: %f", tier, player.Requisition)
			}
			found := false
			for _, entry := range world.SnapshotV2().Players {
				account := entry.(map[string]any)
				if account["id"] == 1 {
					found = true
					if account["requisitionRate"] != 20+pointRate || account["powerRate"] != 0.0 {
						test.Fatalf("HUD disagrees with income: %+v", account)
					}
				}
			}
			if !found {
				test.Fatal("missing player account")
			}
		})
	}
}
func TestGeneratorEnergyCapScalesWithHeadquarters(test *testing.T) {
	world := NewWorld()
	world.SpawnLobbySeat("Blue", 1, "ig", "Arena")
	world.SpawnLobbySeat("Red", 2, "ig", "Arena")
	addGenerators := func(owner, count int, team string) {
		for i := 0; i < count; i++ {
			id := world.allocID()
			world.Structures[id] = &Structure{ID: id, Team: team, Faction: "ig", Owner: owner,
				Type: "thermo_generator", Pos: protocol.Vec3{X: float64(i), Z: 0}, HP: 100, MaxHP: 100}
		}
	}
	if world.powerRateLocked(world.Players[1]) != 0 {
		test.Fatal("energy income must start at zero")
	}
	addGenerators(1, 8, "Blue")
	for tick := 0; tick < 100; tick++ {
		world.Advance(.1)
	}
	player := world.Players[1]
	if math.Abs(player.Energy-160) > .0001 {
		test.Fatalf("eight generators on one HQ must credit six: energy=%f", player.Energy)
	}
	if world.powerRateLocked(player) != 60 {
		test.Fatalf("HUD rate must match credited income: %f", world.powerRateLocked(player))
	}
	found := false
	for _, entry := range world.SnapshotV2().Players {
		if account := entry.(map[string]any); account["id"] == 1 {
			found = true
			if account["powerRate"] != 60.0 {
				test.Fatalf("snapshot disagrees with income: %+v", account)
			}
		}
	}
	if !found {
		test.Fatal("missing player account")
	}
	secondHQ, _ := world.PlaceBuildingFree(1, "Blue", "ig", "hq", protocol.Vec3{X: 50, Z: 50})
	for tick := 0; tick < 100; tick++ {
		world.Advance(.1)
	}
	if math.Abs(player.Energy-240) > .0001 {
		test.Fatalf("second HQ must raise the cap to twelve: energy=%f", player.Energy)
	}
	addGenerators(2, 4, "Red")
	if world.powerRateLocked(world.Players[2]) != 40 {
		test.Fatal("enemy generators must not leak into the ally account")
	}
	world.Buildings[secondHQ].Constructing = true
	if world.powerRateLocked(player) != 60 {
		test.Fatal("an HQ under construction must not raise the cap")
	}
}

func TestHqRecruitCosts(test *testing.T) {
	world := NewWorld()
	world.SpawnLobbySeat("Blue", 1, "space marines", "Arena")
	world.SpawnLobbySeat("Red", 2, "chaos", "Arena")
	headquarters := map[int]int{}
	for id, building := range world.Buildings {
		if building.Type == "hq" {
			headquarters[building.Owner] = id
		}
	}
	if !world.TrainUnit(1, headquarters[1], "scout") || math.Abs(world.Players[1].Requisition-925) > .0001 {
		test.Fatalf("scout must cost 75: %+v", world.Players[1])
	}
	if !world.TrainUnit(2, headquarters[2], "cultist") || math.Abs(world.Players[2].Requisition-950) > .0001 {
		test.Fatalf("cultist must cost 50: %+v", world.Players[2])
	}
}

func TestLobbyFactionChangeAppliesStartingResources(test *testing.T) {
	world := NewWorld()
	world.SpawnLobbySeat("Blue", 1, "ig", "Arena")
	world.SetFaction(1, "Necrons")
	if world.Players[1].Requisition != 1500 {
		test.Fatal("lost Necron exception")
	}
	world.SetFaction(1, "Chaos")
	if world.Players[1].Requisition != 1000 || world.Players[1].Energy != 100 {
		test.Fatal("new lobby race did not get its starting resources")
	}
}
