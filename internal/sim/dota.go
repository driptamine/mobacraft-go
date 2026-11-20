package sim

import (
	_ "embed"
	"encoding/json"
	"math"

	"grimcraft-go/internal/protocol"
)

//go:embed data/dota-units.json
var dotaUnitData []byte

type dotaUnitStats struct {
	HP, Range, Speed float64
	Melee            bool
}

var dotaUnits = func() struct {
	Heroes map[string]dotaUnitStats
	Creeps map[string]dotaUnitStats
} {
	var units struct {
		Heroes map[string]dotaUnitStats
		Creeps map[string]dotaUnitStats
	}
	if err := json.Unmarshal(dotaUnitData, &units); err != nil {
		panic(err)
	}
	return units
}()

func IsDotaMap(name string) bool {
	switch mapNameKey(name) {
	case "dota2", "dotamapgame", "dotamap":
		return true
	default:
		return false
	}
}

func ValidDotaHero(hero string) bool {
	_, found := dotaUnits.Heroes[hero]
	return found
}

func DefaultDotaHero(team string) string {
	if team == "Red" {
		return "lina"
	}
	return "axe"
}

type DotaSeat struct {
	Owner    int
	Team     string
	Hero     string
	Computer bool
}

type dotaLaneWalker struct {
	team, lane string
	points     []protocol.Vec3
	next       int
}

type dotaRound struct {
	lanes    map[string]map[string][]protocol.Vec3
	bases    map[string]int
	walkers  map[int]*dotaLaneWalker
	nextWave float64
}

const dotaWavePeriod = 30.0
const dotaLaneLimit = 24

// StartDotaMatch uses the layout embedded from the client. Pregame players have
// no RTS entities, so the first authoritative snapshot contains only this mode.
func (world *World) StartDotaMatch(seats []DotaSeat) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	world.resetDotaLocked()
	layout := layoutFor("Dota 2")
	world.pointsSeeded = true
	world.mapBounds = layout.Bounds
	world.dota = &dotaRound{lanes: layout.Lanes, bases: map[string]int{}, walkers: map[int]*dotaLaneWalker{}, nextWave: dotaWavePeriod}
	for _, seat := range seats {
		player := world.ensurePlayerLocked(seat.Owner, seat.Team, "ig")
		player.Computer = seat.Computer
		for _, base := range layout.Positions {
			if lowerTeam(seat.Team) != base.Team {
				continue
			}
			id := world.allocID()
			world.Buildings[id] = &Building{ID: id, Type: "hq", HeroBase: true, Owner: seat.Owner, Team: seat.Team, Faction: "ig",
				Pos: protocol.Vec3{X: base.X, Z: base.Z}, HP: 2000, MaxHP: 2000, Tier: 1}
			world.dota.bases[seat.Team] = id
			spot := dotaNav.snap(layout.HeroStarts[base.Team])
			hero := world.makeDotaUnitLocked(seat.Owner, seat.Team, seat.Hero, spot, dotaUnits.Heroes[seat.Hero])
			if seat.Computer {
				// A lane opponent advances and fights using the shared combat loop.
				hero.Computer = true
				world.setDotaLaneLocked(hero, "mid")
				if route := world.dota.walkers[hero.ID]; route != nil {
					closest := 0
					for i, point := range route.points {
						if dist(spot, point) < dist(spot, route.points[closest]) {
							closest = i
						}
					}
					route.next = min(closest+1, len(route.points)-1)
				}
			}
			break
		}
	}
	world.spawnDotaWaveLocked()
}

func (world *World) resetDotaLocked() {
	world.dota = nil
	clear(world.Squads)
	clear(world.Buildings)
	clear(world.Builders)
	clear(world.Structures)
	clear(world.Vehicles)
	clear(world.Points)
	clear(world.Players)
	world.navigation = navigationState{}
	world.Winner = ""
	world.Tick = 0
}

func (world *World) ResetDotaMatch() {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	world.resetDotaLocked()
}

func (world *World) makeDotaUnitLocked(owner int, team, unit string, position protocol.Vec3, stats dotaUnitStats) *Squad {
	id := world.allocID()
	direction := 1.0
	if team == "Red" {
		direction = -1
	}
	mode, projectile := "ranged", "Bullet"
	if stats.Melee {
		mode = "melee"
		projectile = ""
	}
	squad := &Squad{ID: id, Owner: owner, Team: team, Faction: "ig", UnitType: unit, Pos: position,
		Facing: protocol.Vec3{X: direction, Z: direction}, Speed: stats.Speed,
		AttackRange: stats.Range, DetectRange: math.Max(14, stats.Range+2), Stopped: true,
		AttackMode: mode, Stance: "ranged", ProjectileType: projectile,
		Soldiers: []Soldier{{EntityID: world.allocID(), UnitType: unit, FormIdx: 1, Alive: true, HP: stats.HP, MaxHP: stats.HP}}}
	world.Squads[id] = squad
	return squad
}

func (world *World) setDotaLaneLocked(squad *Squad, lane string) {
	points := world.dota.lanes[lowerTeam(squad.Team)][lane]
	if len(points) == 0 {
		return
	}
	squad.AutoChase, squad.AttackMove, squad.Stopped = true, true, false
	world.dota.walkers[squad.ID] = &dotaLaneWalker{team: squad.Team, lane: lane, points: points}
}

func (world *World) spawnDotaWaveLocked() {
	for _, team := range []string{"Blue", "Red"} {
		if world.Buildings[world.dota.bases[team]] == nil {
			continue
		}
		side, owner := "radiant", -900001
		if team == "Red" {
			side, owner = "dire", -900002
		}
		for _, lane := range []string{"top", "mid", "bottom"} {
			points := world.dota.lanes[lowerTeam(team)][lane]
			if len(points) < 2 {
				continue
			}
			count := 0
			for id, walker := range world.dota.walkers {
				if squad := world.Squads[id]; squad != nil && squad.Owner == owner && walker.lane == lane {
					count++
				}
			}
			start, ahead := points[0], points[1]
			direction := math.Max(.1, dist(start, ahead))
			for index, kind := range []string{"melee", "melee", "melee", "ranged", "siege"} {
				if count+index >= dotaLaneLimit {
					break
				}
				name := side + "_" + kind + "_creep"
				offset := float64(index) * 2.5 / direction
				position := dotaNav.snap(protocol.Vec3{X: start.X - (ahead.X-start.X)*offset, Z: start.Z - (ahead.Z-start.Z)*offset})
				squad := world.makeDotaUnitLocked(owner, team, name, position, dotaUnits.Creeps[name])
				squad.Computer = true
				world.setDotaLaneLocked(squad, lane)
				world.dota.walkers[squad.ID].next = 1
			}
		}
	}
}

func (world *World) advanceDotaLocked(dt float64) {
	if world.Winner != "" {
		return
	}
	world.dota.nextWave -= dt
	if world.dota.nextWave <= 1e-7 {
		world.spawnDotaWaveLocked()
		world.dota.nextWave += dotaWavePeriod
	}
	for id, walker := range world.dota.walkers {
		squad := world.Squads[id]
		if squad == nil {
			delete(world.dota.walkers, id)
			delete(world.navigation.Paths, id)
			continue
		}
		if squad.MoveOrder != nil {
			continue
		}
		// Existing combat pursues nearby enemies. Wait here until that fight
		// finishes, then restore the authored waypoint.
		world.acquireTargetLocked(squad)
		if squad.ForcedTarget != nil {
			continue
		}
		for walker.next < len(walker.points) && dist(squad.Pos, dotaNav.snap(walker.points[walker.next])) < 1.5 {
			walker.next++
		}
		if walker.next < len(walker.points) {
			target := dotaNav.snap(walker.points[walker.next])
			squad.MoveOrder = &target
			squad.Stopped = false
		} else {
			enemy := "Blue"
			if walker.team == "Blue" {
				enemy = "Red"
			}
			id := world.dota.bases[enemy]
			squad.ForcedBuilding = &id
		}
	}
}

func (world *World) updateDotaWinnerLocked() {
	for _, team := range []string{"Blue", "Red"} {
		if base := world.Buildings[world.dota.bases[team]]; base == nil || base.HP <= 0 {
			if team == "Blue" {
				world.Winner = "red"
			} else {
				world.Winner = "blue"
			}
			return
		}
	}
}
