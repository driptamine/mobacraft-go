package sim

import (
	"grimcraft-go/internal/protocol"
	"math"
	"testing"
)

func TestGroundUnitsRouteAroundBuildings(test *testing.T) {
	for _, kind := range []string{"squad", "builder", "vehicle", "apc"} {
		for _, constructing := range []bool{false, true} {
			test.Run(kind+map[bool]string{false: "-complete", true: "-construction"}[constructing], func(test *testing.T) {
				world := NewWorld()
				world.EnsurePlayer(1, "Blue", "ig")
				siteID, _ := world.PlaceBuildingFree(2, "Red", "ig", "hq", protocol.Vec3{})
				site := world.Buildings[siteID]
				site.Constructing = constructing
				site.RequiresWorker = constructing
				site.ConstructTime = 10000
				goal := protocol.Vec3{X: 30}
				radius := .5
				var position func() protocol.Vec3
				var pending func() bool
				switch kind {
				case "squad":
					squad := world.SpawnSquad("Blue", 1)
					squad.Pos = protocol.Vec3{X: -30}
					squad.MoveOrder = &goal
					squad.Stopped = false
					position = func() protocol.Vec3 { return squad.Pos }
					pending = func() bool { return squad.MoveOrder != nil }
				case "builder":
					worker := world.SpawnBuilder("Blue", 1, "ig", -30, 0)
					worker.MoveTarget = &goal
					position = func() protocol.Vec3 { return worker.Pos }
					pending = func() bool { return worker.MoveTarget != nil }
				default:
					radius = 1
					if kind == "apc" {
						radius = 1.2
					}
					id := world.allocID()
					vehicle := &Vehicle{ID: id, Kind: kind, Owner: 1, Team: "Blue", Faction: "ig", HP: 100, Pos: protocol.Vec3{X: -30}, MoveOrder: &goal}
					world.Vehicles[id] = vehicle
					position = func() protocol.Vec3 { return vehicle.Pos }
					pending = func() bool { return vehicle.MoveOrder != nil }
				}
				detoured := false
				for tick := 0; tick < 800 && pending(); tick++ {
					world.Advance(.05)
					actual := position()
					if insideFootprint(actual, structureFootprint("ig", "hq", constructing, site.Pos), radius-.01) {
						test.Fatalf("unit entered building at %+v", actual)
					}
					if math.Abs(actual.Z) > 5 {
						detoured = true
					}
				}
				if pending() || dist(position(), goal) > 1 || !detoured {
					test.Fatalf("unit did not finish detour: %+v", position())
				}
			})
		}
	}
}
func TestGroundRouteReplansConstructionAndRemoval(test *testing.T) {
	world := NewWorld()
	worker := world.SpawnBuilder("Blue", 1, "ig", -30, 0)
	goal := protocol.Vec3{X: 30}
	worker.MoveTarget = &goal
	world.Advance(.1)
	siteID, _ := world.PlaceBuildingFree(2, "Red", "ig", "hq", protocol.Vec3{})
	site := world.Buildings[siteID]
	for tick := 0; tick < 300 && worker.MoveTarget != nil; tick++ {
		world.Advance(.05)
		if insideFootprint(worker.Pos, structureFootprint(site.Faction, site.Type, false, site.Pos), .49) {
			test.Fatal("stale route crossed new building")
		}
	}
	if worker.MoveTarget != nil {
		test.Fatal("worker stuck on new building")
	}
	delete(world.Buildings, siteID)
	worker.MoveTarget = &protocol.Vec3{X: -30}
	world.Advance(.1)
	if !groundLineClear(worker.Pos, *worker.MoveTarget, world.navigation.Bounds, false) {
		test.Fatal("demolished building still blocks route")
	}
}
func TestGroundOrderInsideBuildingStopsOutside(test *testing.T) {
	world := NewWorld()
	_, _ = world.PlaceBuildingFree(2, "Red", "chaos", "hq", protocol.Vec3{})
	worker := world.SpawnBuilder("Blue", 1, "ig", -20, 0)
	worker.MoveTarget = &protocol.Vec3{}
	for tick := 0; tick < 300 && worker.MoveTarget != nil; tick++ {
		world.Advance(.05)
	}
	if worker.MoveTarget != nil || worker.Pos.X >= -5 || math.Abs(worker.Pos.Z) > 1 {
		test.Fatalf("unreachable building order: %+v", worker)
	}
}
func TestGroundPursuitAndVehicleClearance(test *testing.T) {
	world := NewWorld()
	world.PlaceBuildingFree(2, "Red", "chaos", "hq", protocol.Vec3{})
	hunter := world.SpawnSquad("Blue", 1)
	target := world.SpawnSquad("Red", 2)
	hunter.Pos = protocol.Vec3{X: -20}
	target.Pos = protocol.Vec3{X: 20}
	hunter.AttackMode = "melee"
	hunter.ForcedTarget = &target.ID
	hunter.Stopped = false
	target.Stopped = true
	target.AttackRange = 0
	for tick := 0; tick < 800 && dist(hunter.Pos, target.Pos) > MeleeRange; tick++ {
		world.Advance(.025)
		if insideFootprint(hunter.Pos, structureFootprint("chaos", "hq", false, protocol.Vec3{}), .49) {
			test.Fatal("pursuit entered building")
		}
	}
	if dist(hunter.Pos, target.Pos) > MeleeRange {
		test.Fatalf("pursuit stopped at building: %+v", hunter.Pos)
	}
	gap := []footprintBounds{{-5, 5, -20, -.8}, {-5, 5, .8, 20}}
	start, goal := protocol.Vec3{X: -15}, protocol.Vec3{X: 15}
	if !groundLineClear(start, goal, expandGroundBounds(gap, .5), false) {
		test.Fatal("infantry cannot use a wide enough gap")
	}
	route := groundBuildingPath(start, goal, expandGroundBounds(gap, 1.2))
	previous := start
	for _, point := range route {
		if !groundLineClear(previous, point, expandGroundBounds(gap, 1.2), false) {
			test.Fatal("vehicle squeezed through an undersized gap")
		}
		previous = point
	}
}

func TestReplicaFormationStaysOutsideBuildings(test *testing.T) {
	world := NewWorld()
	world.PlaceBuildingFree(2, "Red", "chaos", "hq", protocol.Vec3{})
	squad := world.SpawnSquad("Blue", 1)
	squad.Pos = protocol.Vec3{X: -5.8, Z: 0}
	snapshot := world.SnapshotV2()
	checked := 0
	for _, entity := range snapshot.Entities {
		record := entity.(map[string]any)
		components := record["components"].(map[string]any)
		if _, soldier := components["soldier"]; !soldier {
			continue
		}
		transform := record["transform"].(map[string]any)
		position := transform["p"].([]float64)
		if insideFootprint(protocol.Vec3{X: position[0], Z: position[2]}, structureFootprint("chaos", "hq", false, protocol.Vec3{}), .49) {
			test.Fatal("replicated formation places a soldier inside a building")
		}
		checked++
	}
	if checked == 0 {
		test.Fatal("no soldier replicas checked")
	}
}
