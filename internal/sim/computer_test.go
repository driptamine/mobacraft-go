package sim

import (
	"grimcraft-go/internal/botai"
	"grimcraft-go/internal/protocol"
	"math"
	"testing"
)

func TestComputerBuildsCapturesAndControlsProduction(test *testing.T) {
	for _, faction := range []string{"ig", "chaos", "space marines", "orks", "necrons", "eldar", "dark eldar", "tau", "sob"} {
		test.Run(faction, func(test *testing.T) {
			world := NewWorld()
			world.PlaceBuildingFree(1, "Blue", "ig", "hq", protocol.Vec3{X: -400, Z: -400})
			world.AddComputer("Red", 2, faction, "Arena")
			if len(world.Squads) != 0 || len(world.Builders) != 1 {
				test.Fatal("computer received a free starting army")
			}
			for tick := 0; tick < 1600; tick++ {
				world.Advance(.1)
				if world.Players[2].Requisition < 0 {
					test.Fatal("computer spent unavailable resources")
				}
			}
			owned, captured, workers, generators := 0, 0, 0, 0
			for _, squad := range world.Squads {
				if squad.Owner == 2 && aliveCount(squad) > 0 {
					owned++
					if !squad.Computer {
						test.Fatal("produced army is not controlled by AI")
					}
				}
			}
			for _, point := range world.Points {
				if point.Owner == "Red" {
					captured++
				}
			}
			for _, worker := range world.Builders {
				if worker.Owner == 2 {
					workers++
					if worker.Faction != faction {
						test.Fatal("worker lost its faction")
					}
				}
			}
			for _, structure := range world.Structures {
				if structure.Owner == 2 && !structure.Constructing {
					generators++
				}
			}
			if owned < 3 || captured < 1 || workers < 2 {
				test.Fatalf("inactive bot: squads=%d captured=%d workers=%d", owned, captured, workers)
			}
			if faction != "dark eldar" && generators == 0 {
				test.Fatal("computer did not finish a generator with a worker")
			}
			if math.Abs(world.Players[1].Requisition-1320) > .01 || world.Players[1].Energy != 100 {
				test.Fatal("computer spending or income affected human economy")
			}
		})
	}
}

func TestComputerDefendsAndRetreatsInsteadOfOverwritingHumanOrders(test *testing.T) {
	world := NewWorld()
	world.AddComputer("Red", 2, "ig", "Arena")
	var headquartersID int
	for _, building := range world.Buildings {
		if building.Owner == 2 && building.Type == "hq" {
			headquartersID = building.ID
		}
	}
	if !world.TrainUnit(2, headquartersID, "squad") {
		test.Fatal("could not order a defending squad")
	}
	for tick := 0; tick < 40; tick++ {
		world.Advance(.1)
	}
	if len(world.computerSquads(2)) == 0 {
		test.Fatal("HQ did not produce a defending squad")
	}
	state := world.computerPlayers[2]
	enemy := world.SpawnSquad("Blue", 1)
	enemy.Pos = protocol.Vec3{X: state.Base.X + 30, Z: state.Base.Z}
	humanOrder := protocol.Vec3{X: -400, Z: -300}
	enemy.MoveOrder = &humanOrder
	world.Advance(.5)
	for _, squad := range world.computerSquads(2) {
		if squad.ForcedTarget == nil || *squad.ForcedTarget != enemy.ID || squad.CaptureOrderSP != nil {
			test.Fatal("computer did not defend its base")
		}
	}
	if enemy.MoveOrder == nil || enemy.MoveOrder.X != humanOrder.X {
		test.Fatal("computer overwrote a human order")
	}
	for _, squad := range world.computerSquads(2) {
		for index := range squad.Soldiers {
			squad.Soldiers[index].HP = 1
		}
		world.Advance(.5)
		if squad.MoveOrder == nil || dist(*squad.MoveOrder, state.Base) > 20 {
			test.Fatal("damaged squad did not retreat")
		}
		break
	}
}

func TestComputerDoesNotSpawnFreeReplacementArmies(test *testing.T) {
	world := NewWorld()
	world.AddComputer("Red", 2, "chaos", "Arena")
	world.Players[2].Requisition = 0
	for _, squad := range world.Squads {
		for index := range squad.Soldiers {
			squad.Soldiers[index].Alive = false
			squad.Soldiers[index].HP = 0
		}
	}
	for tick := 0; tick < 200; tick++ {
		world.Advance(.1)
	}
	if len(world.computerSquads(2)) != 0 {
		test.Fatal("computer spawned without paying and training")
	}
}

func TestComputerFortifiesPointsForEveryRace(test *testing.T) {
	for faction, post := range map[string]string{
		"ig": "listening_post", "chaos": "listening_post", "space marines": "listening_post",
		"orks": "listening_post", "necrons": "listening_post", "eldar": "listening_post",
		"tau": "tau_listening_post", "sob": "listening_post",
	} {
		test.Run(faction, func(test *testing.T) {
			world := NewWorld()
			world.SpawnLobbySeat("Blue", 1, "ig", "Arena")
			world.AddComputer("Red", 2, faction, "Arena")
			for _, point := range world.Points {
				if point.Kind != "relic" {
					point.Owner = "Red"
					break
				}
			}
			for tick := 0; tick < 2000; tick++ {
				world.Advance(.1)
			}
			for _, building := range world.Buildings {
				if building.Owner == 2 && building.Type == post && building.StrategicPointID != nil {
					return
				}
			}
			test.Fatalf("%s computer placed no %s on its point", faction, post)
		})
	}
	if botai.ListeningPostType(botai.Settings.Factions["dark eldar"]) != "" {
		test.Fatal("dark eldar has no listening post data in ai/")
	}
}

func TestComputerDoesNotStealHumanListeningPosts(test *testing.T) {
	world := NewWorld()
	world.SpawnLobbySeat("Blue", 1, "ig", "Arena")
	world.SpawnLobbySeat("Red", 2, "ig", "Arena")
	world.AddComputer("Blue", 3, "tau", "Arena")
	ally := world.Players[3]
	var target *Point
	for _, point := range world.Points {
		if point.Kind != "relic" {
			target = point
			break
		}
	}
	target.Owner = "Blue"
	human := world.SpawnBuilder("Blue", 1, "ig", target.Pos.X, target.Pos.Z)
	foe := world.SpawnBuilder("Red", 2, "ig", target.Pos.X, target.Pos.Z)
	// A human order walking to the point blocks the ally.
	human.BuildSP = &target.ID
	if !world.lpClaimedByTeammateLocked(ally, target.ID) {
		test.Fatal("ally must respect a human construction order")
	}
	// So does a human post already under construction on it.
	human.BuildSP = nil
	site := world.allocID()
	world.Buildings[site] = &Building{ID: site, Team: "Blue", Faction: "ig", Owner: 1, StrategicPointID: &target.ID}
	human.BuildTarget = &site
	if !world.lpClaimedByTeammateLocked(ally, target.ID) {
		test.Fatal("ally must respect a human post under construction")
	}
	// Enemies, the ally's own builders and untouched points never block.
	human.BuildTarget = nil
	foe.BuildSP = &target.ID
	own := world.SpawnBuilder("Blue", 3, "tau", target.Pos.X, target.Pos.Z)
	own.BuildSP = &target.ID
	if world.lpClaimedByTeammateLocked(ally, target.ID) {
		test.Fatal("enemy and own orders must not block the ally")
	}
	foe.BuildSP = nil
	own.BuildSP = nil
	delete(world.Buildings, site)
	if world.lpClaimedByTeammateLocked(ally, target.ID) {
		test.Fatal("untouched point must stay buildable")
	}
}

func TestFortifiedListeningPostFiresBolters(test *testing.T) {
	world := NewWorld()
	world.SpawnLobbySeat("Blue", 1, "ig", "Arena")
	world.SpawnLobbySeat("Red", 2, "ig", "Arena")
	var target *Point
	for _, point := range world.Points {
		if point.Kind != "relic" {
			target = point
			break
		}
	}
	target.Owner = "Blue"
	site := world.allocID()
	world.Buildings[site] = &Building{ID: site, Team: "Blue", Faction: "ig", Owner: 1,
		Type: "listening_post", Pos: target.Pos, HP: 400, MaxHP: 400, StrategicPointID: &target.ID}
	target.HasListeningPost = true
	target.ListeningPostEntityID = &site
	enemy := world.SpawnSquad("Red", 2)
	enemy.Pos = protocol.Vec3{X: target.Pos.X + 10, Z: target.Pos.Z}
	health := func() float64 {
		value := 0.0
		for _, soldier := range enemy.Soldiers {
			if soldier.Alive {
				value += soldier.HP
			}
		}
		return value
	}
	before := health()
	for tick := 0; tick < 30; tick++ {
		world.Advance(.1)
	}
	if health() != before {
		test.Fatal("tier 1 post must hold fire")
	}
	target.LPTier = 2
	for tick := 0; tick < 30 && health() == before; tick++ {
		world.Advance(.1)
	}
	if health() == before {
		test.Fatal("tier 2 post must fire its bolters")
	}
}

func TestAlliedComputersPreserveHumanControllers(test *testing.T) {
	world := NewWorld()
	world.SpawnLobbySeat("Blue", 1, "ig", "Arena")
	world.SpawnLobbySeat("Blue", 2, "space marines", "Arena")
	world.AddComputer("Blue", 3, "tau", "Arena")
	world.SpawnLobbySeat("Red", 4, "chaos", "Arena")
	for tick := 0; tick < 600; tick++ {
		world.Advance(.1)
	}
	if world.Players[1].Computer || world.Players[2].Computer || !world.Players[3].Computer {
		test.Fatal("human/computer ownership changed")
	}
	if len(world.computerSquads(3)) < 2 {
		test.Fatal("allied computer did not produce its army")
	}
	for _, squad := range world.Squads {
		if squad.Owner == 1 || squad.Owner == 2 {
			test.Fatal("AI produced an army for a human")
		}
	}
	for _, worker := range world.Builders {
		if (worker.Owner == 1 || worker.Owner == 2) && (worker.BuildTarget != nil || worker.MoveTarget != nil || worker.IsBuilding) {
			test.Fatal("AI commanded a human worker")
		}
	}
	for _, owner := range []int{1, 2} {
		if world.Players[owner].Requisition < 1120-1e-5 || world.Players[owner].Energy != 100 {
			test.Fatal("allied computer spending affected a human account")
		}
	}
}
