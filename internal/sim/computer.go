package sim

import (
	"grimcraft-go/internal/botai"
	"grimcraft-go/internal/protocol"
	"math"
	"sort"
)

type computerPlayer struct {
	Base       protocol.Vec3
	Timer      float64
	Opening    int
	Placement  int
	Retreating map[int]bool
}

func (world *World) SetComputerDifficulty(owner int, difficulty string) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	if !botai.ValidDifficulty(difficulty) {
		difficulty = "standard"
	}
	if player := world.Players[owner]; player != nil && player.Computer {
		player.Difficulty = difficulty
	}
}

func sortedEntityIDs[T any](entities map[int]T) []int {
	result := make([]int, 0, len(entities))
	for entityID := range entities {
		result = append(result, entityID)
	}
	sort.Ints(result)
	return result
}

func (world *World) advanceComputersLocked(deltaTime float64) {
	if world.computerPlayers == nil {
		world.computerPlayers = map[int]*computerPlayer{}
	}
	for _, owner := range sortedEntityIDs(world.Players) {
		player := world.Players[owner]
		if !player.Computer {
			continue
		}
		state := world.computerPlayers[owner]
		if state == nil {
			state = &computerPlayer{Timer: botai.Settings.ThinkInterval, Retreating: map[int]bool{}}
			for _, id := range sortedEntityIDs(world.Buildings) {
				building := world.Buildings[id]
				if building.Owner == owner && building.Type == "hq" {
					state.Base = building.Pos
					break
				}
			}
			world.computerPlayers[owner] = state
		}
		state.Timer += deltaTime
		if state.Timer < botai.Settings.ThinkInterval {
			continue
		}
		state.Timer = math.Mod(state.Timer, botai.Settings.ThinkInterval)
		profile := botai.For(player.Faction, player.Difficulty)
		world.developComputerLocked(player, state, profile)
		world.commandComputerArmyLocked(player, state, profile)
	}
}

func (world *World) computerSquads(owner int) []*Squad {
	result := []*Squad{}
	for _, id := range sortedEntityIDs(world.Squads) {
		squad := world.Squads[id]
		if squad.Owner == owner && aliveCount(squad) > 0 {
			result = append(result, squad)
		}
	}
	return result
}

func (world *World) computerTrainLocked(player *Player, unitType string) bool {
	catalog := botai.Settings.Factions[player.Faction]
	if len(catalog.Units) == 0 {
		return false
	}
	for _, id := range sortedEntityIDs(world.Buildings) {
		building := world.Buildings[id]
		if building.Owner != player.ID || building.Constructing || len(building.Queue) > 0 {
			continue
		}
		if building.Type == "hq" && unitType != "builder" && unitType != catalog.Units[0].Type {
			continue
		}
		if building.Type != "hq" && (unitType == "builder" || botai.IsListeningPost(building.Type)) {
			continue
		}
		if unitType == catalog.Units[0].Type {
			unitType = "squad"
		}
		return world.trainUnitLocked(player.ID, id, unitType)
	}
	return false
}

func (world *World) computerPlacementLocked(player *Player, state *computerPlayer, kind string) *protocol.Vec3 {
	for attempt := 0; attempt < 96; attempt++ {
		angle := float64((state.Placement+attempt)%12) * math.Pi / 6
		radius := 24 + float64(attempt/12)*15
		point := protocol.Vec3{X: state.Base.X + math.Cos(angle)*radius, Z: state.Base.Z + math.Sin(angle)*radius}
		bound := world.mapBounds
		if bound <= 0 {
			bound = 256
		}
		if math.Abs(point.X) > bound-15 || math.Abs(point.Z) > bound-15 {
			continue
		}
		bounds := structureFootprint(player.Faction, kind, true, point)
		overlaps := func(other footprintBounds) bool {
			return bounds.MinX-3 < other.MaxX && bounds.MaxX+3 > other.MinX && bounds.MinZ-3 < other.MaxZ && bounds.MaxZ+3 > other.MinZ
		}
		blocked := false
		for _, building := range world.Buildings {
			if overlaps(structureFootprint(building.Faction, building.Type, building.Constructing, building.Pos)) {
				blocked = true
				break
			}
		}
		if !blocked {
			for _, structure := range world.Structures {
				if overlaps(structureFootprint(structure.Faction, structure.Type, structure.Constructing, structure.Pos)) {
					blocked = true
					break
				}
			}
		}
		if blocked {
			continue
		}
		state.Placement = (state.Placement + attempt + 1) % 12
		return &point
	}
	return nil
}

func (world *World) computerBuildLocked(player *Player, state *computerPlayer, kind string, point *Point) bool {
	if player.Requisition < costOf(kind) {
		return false
	}
	var worker *Builder
	for _, id := range sortedEntityIDs(world.Builders) {
		candidate := world.Builders[id]
		if candidate.Owner == player.ID && candidate.HP > 0 && candidate.BuildTarget == nil && !candidate.IsBuilding {
			worker = candidate
			break
		}
	}
	if worker == nil {
		return false
	}
	at := world.computerPlacementLocked(player, state, kind)
	if point != nil {
		copied := point.Pos
		at = &copied
	}
	if at == nil {
		return false
	}
	_, built := world.placeBuildingLocked(player.ID, worker.ID, kind, *at)
	return built
}

func (world *World) developComputerLocked(player *Player, state *computerPlayer, profile botai.Profile) {
	catalog := botai.Settings.Factions[player.Faction]
	if len(catalog.Units) == 0 {
		return
	}
	squads := world.computerSquads(player.ID)
	workers := []*Builder{}
	for _, id := range sortedEntityIDs(world.Builders) {
		if worker := world.Builders[id]; worker.Owner == player.ID && worker.HP > 0 {
			workers = append(workers, worker)
		}
	}
	count := func(kind, name string) int {
		result := 0
		if kind == "build" {
			for _, building := range world.Buildings {
				if building.Owner == player.ID && building.Type == name {
					result++
				}
			}
			for _, structure := range world.Structures {
				if structure.Owner == player.ID && structure.Type == name {
					result++
				}
			}
			return result
		}
		if name == "builder" {
			result = len(workers)
		} else {
			for _, squad := range squads {
				if squad.UnitType == name || ((squad.UnitType == "" || squad.UnitType == "squad") && name == catalog.Units[0].Type) {
					result++
				}
			}
		}
		for _, building := range world.Buildings {
			if building.Owner == player.ID {
				for _, item := range building.Queue {
					if item.Type == name || (item.Type == "squad" && name == catalog.Units[0].Type) {
						result++
					}
				}
			}
		}
		return result
	}
	// Another living worker resumes an interrupted construction order.
	resume := func(id int, pos protocol.Vec3, faction, kind string) {
		for _, worker := range workers {
			if worker.BuildTarget != nil && *worker.BuildTarget == id {
				return
			}
		}
		for _, worker := range workers {
			if worker.BuildTarget == nil {
				worker.BuildTarget = &id
				worker.BuildType = kind
				worker.BuildPos = &pos
				assignConstructionBuilder(worker, pos, faction, kind)
				return
			}
		}
	}
	for _, id := range sortedEntityIDs(world.Buildings) {
		site := world.Buildings[id]
		if site.Owner == player.ID && site.Constructing {
			resume(id, site.Pos, site.Faction, site.Type)
		}
	}
	for _, id := range sortedEntityIDs(world.Structures) {
		site := world.Structures[id]
		if site.Owner == player.ID && site.Constructing {
			resume(id, site.Pos, site.Faction, site.Type)
		}
	}
	if len(workers) == 0 {
		world.computerTrainLocked(player, "builder")
		return
	}
	if count("build", "hq") == 0 {
		world.computerBuildLocked(player, state, "hq", nil)
		return
	}
	// Keep the minimum workforce ahead of expensive opening commanders.
	if count("train", "builder") < profile.Engineers && world.computerTrainLocked(player, "builder") {
		return
	}
	for skipped := 0; skipped < len(profile.Opening) && state.Opening < len(profile.Opening); skipped++ {
		order := profile.Opening[state.Opening]
		if order.Type == "" {
			state.Opening++
			continue
		}
		desired := 1
		for _, previous := range profile.Opening[:state.Opening] {
			if previous.Kind == order.Kind && previous.Type == order.Type {
				desired++
			}
		}
		if count(order.Kind, order.Type) >= desired {
			state.Opening++
			continue
		}
		performed := false
		if order.Kind == "build" {
			performed = world.computerBuildLocked(player, state, order.Type, nil)
		} else {
			performed = world.computerTrainLocked(player, order.Type)
		}
		if performed {
			state.Opening++
		}
		return
	}
	for _, entry := range catalog.Structures {
		if entry.Generator {
			desired := min(3, max(1, len(squads)/2))
			if count("build", entry.Type) < desired {
				// Save for power instead of starving it with cheaper troop orders.
				world.computerBuildLocked(player, state, entry.Type, nil)
				return
			}
			break
		}
	}
	if listeningPost := botai.ListeningPostType(catalog); listeningPost != "" {
		for _, id := range sortedEntityIDs(world.Points) {
			point := world.Points[id]
			if point.Kind == "relic" || point.Owner != player.Team || point.HasListeningPost || point.ListeningPostEntityID != nil {
				continue
			}
			if world.lpClaimedByTeammateLocked(player, id) {
				continue
			}
			if world.computerBuildLocked(player, state, listeningPost, point) {
				return
			}
		}
	}
	queued := 0
	for _, building := range world.Buildings {
		if building.Owner == player.ID {
			for _, item := range building.Queue {
				if item.Type != "builder" {
					queued++
				}
			}
		}
	}
	// Dedicated capturers first: 2 scouts for Space Marines, the profile
	// quota otherwise. Main squads of Space Marines stop at 3.
	if capper := capperTypeFor(player.Faction); capper != "" {
		want := profile.Capturers
		if player.Faction == "space marines" {
			want = 2
		}
		if count("train", capper) < want && world.computerTrainLocked(player, capper) {
			return
		}
	}
	mainMax := 1 << 30
	if player.Faction == "space marines" {
		mainMax = 3
	}
	target := min(12, max(profile.MinAttackSquads+profile.Capturers, int(profile.ArmyBudget/math.Max(1, catalog.Units[0].Cost))))
	if len(squads)+queued < target && count("train", catalog.Units[0].Type) < mainMax {
		trained := world.computerTrainLocked(player, catalog.Units[0].Type)
		if !trained && len(squads)+queued < min(target, profile.MinAttackSquads+profile.Capturers) {
			return
		}
	}
	// Continue the race's tech progression past the opening (wiki build
	// order), one structure at a time once the minimum attack force stands.
	if len(squads)+queued >= min(target, profile.MinAttackSquads+profile.Capturers) {
		for _, kind := range catalog.TechOrder {
			if count("build", kind) == 0 && world.computerBuildLocked(player, state, kind, nil) {
				return
			}
		}
	}
	for _, squad := range squads {
		maximum := min(13, catalog.Units[0].MaxSize)
		if aliveCount(squad) < maximum && len(squad.ReinforceQueue) == 0 {
			world.queueReinforceAtLocked(player.ID, squad.ID, 0)
		}
	}
}

// lpClaimedByTeammateLocked keeps an ally from fortifying a point another
// same-team builder is already raising a post on — first order wins, so a
// human teammate's construction is never stolen by AI.
func (world *World) lpClaimedByTeammateLocked(player *Player, pointID int) bool {
	for _, builder := range world.Builders {
		if builder.Owner == player.ID || builder.Team != player.Team {
			continue
		}
		if builder.BuildSP != nil && *builder.BuildSP == pointID {
			return true
		}
		if builder.BuildTarget != nil {
			if site, ok := world.Buildings[*builder.BuildTarget]; ok && site.StrategicPointID != nil && *site.StrategicPointID == pointID {
				return true
			}
		}
	}
	return false
}

// capperTypeFor names the dedicated capture squad per race: scouts for
// Space Marines, cultists for Chaos. Other races capture with line troops.
func capperTypeFor(faction string) string {
	switch faction {
	case "space marines":
		return "scout"
	case "chaos":
		return "cultist"
	}
	return ""
}

func computerMove(squad *Squad, target protocol.Vec3, pointID, enemyID int) {
	if pointID != 0 && squad.CaptureOrderSP != nil && *squad.CaptureOrderSP == pointID {
		return
	}
	squad.CaptureOrderSP = nil
	squad.ForcedTarget = nil
	squad.ForcedBuilding = nil
	squad.Stopped = false
	squad.AttackMove = enemyID != 0
	squad.AutoChase = enemyID != 0
	if pointID != 0 {
		squad.CaptureOrderSP = &pointID
	}
	if enemyID != 0 {
		squad.ForcedTarget = &enemyID
	}
	if squad.MoveOrder == nil || dist(*squad.MoveOrder, target) > 2 {
		copied := target
		squad.MoveOrder = &copied
	}
}

func (world *World) commandComputerArmyLocked(player *Player, state *computerPlayer, profile botai.Profile) {
	squads := world.computerSquads(player.ID)
	// Slugga boyz force melee; shoota squads keep firing from range.
	if player.Faction == "orks" {
		for _, squad := range squads {
			if squad.UnitType != "boyz_shoota" {
				squad.AttackMode = "melee"
			}
		}
	}
	// Dedicated capturers take the capture slots first.
	if capper := capperTypeFor(player.Faction); capper != "" {
		sort.SliceStable(squads, func(i, j int) bool {
			return squads[i].UnitType == capper && squads[j].UnitType != capper
		})
	}
	enemies := []*Squad{}
	for _, id := range sortedEntityIDs(world.Squads) {
		enemy := world.Squads[id]
		if enemy.Team != player.Team && aliveCount(enemy) > 0 {
			enemies = append(enemies, enemy)
		}
	}
	nearest := func(from protocol.Vec3) *Squad {
		var target *Squad
		for _, enemy := range enemies {
			if target == nil || dist(from, enemy.Pos) < dist(from, target.Pos) {
				target = enemy
			}
		}
		return target
	}
	baseThreat := nearest(state.Base)
	if baseThreat != nil && dist(state.Base, baseThreat.Pos) >= 40 {
		baseThreat = nil
	}
	reserved := map[int]bool{}
	for _, squad := range world.Squads {
		if squad.Owner != player.ID && squad.Team == player.Team && squad.CaptureOrderSP != nil {
			reserved[*squad.CaptureOrderSP] = true
		}
	}
	owned := 0
	for _, point := range world.Points {
		if point.Owner == player.Team {
			owned++
		}
	}
	capturers := map[int]bool{}
	for _, squad := range squads {
		health, maximum := 0.0, 0.0
		for _, soldier := range squad.Soldiers {
			if soldier.Alive {
				health += soldier.HP
				maximum += math.Max(1, soldier.MaxHP)
			}
		}
		health /= math.Max(1, maximum)
		if health < .35 {
			state.Retreating[squad.ID] = true
		}
		if state.Retreating[squad.ID] {
			if health > .75 {
				delete(state.Retreating, squad.ID)
			} else {
				computerMove(squad, state.Base, 0, 0)
				continue
			}
		}
		if baseThreat != nil {
			computerMove(squad, baseThreat.Pos, 0, baseThreat.ID)
			continue
		}
		if owned >= int(math.Ceil(float64(len(world.Points))*profile.PointLimit)) || len(capturers) >= profile.Capturers {
			continue
		}
		var target *Point
		if squad.CaptureOrderSP != nil {
			point := world.Points[*squad.CaptureOrderSP]
			if point != nil && point.Owner != player.Team && !point.HasListeningPost && !reserved[point.ID] {
				target = point
			}
		}
		if target == nil {
			for _, id := range sortedEntityIDs(world.Points) {
				point := world.Points[id]
				if point.Owner == player.Team || point.HasListeningPost || reserved[id] {
					continue
				}
				danger := false
				for _, enemy := range enemies {
					if dist(point.Pos, enemy.Pos) < 20 {
						danger = true
						break
					}
				}
				if danger {
					continue
				}
				if target == nil || dist(squad.Pos, point.Pos) < dist(squad.Pos, target.Pos) {
					target = point
				}
			}
		}
		if target != nil {
			computerMove(squad, target.Pos, target.ID, 0)
			reserved[target.ID] = true
			capturers[squad.ID] = true
		}
	}
	if baseThreat != nil {
		return
	}
	army := []*Squad{}
	for _, squad := range squads {
		if !capturers[squad.ID] && !state.Retreating[squad.ID] {
			army = append(army, squad)
		}
	}
	strength := func(units []*Squad) float64 {
		value := 0.0
		for _, squad := range units {
			catalog := botai.Settings.Factions[squad.Faction]
			cost := 100.0
			if len(catalog.Units) > 0 {
				cost = catalog.Units[0].Cost
			}
			for _, soldier := range squad.Soldiers {
				if soldier.Alive {
					value += cost * soldier.HP / math.Max(1, soldier.MaxHP) / 5
				}
			}
		}
		return value
	}
	ownStrength := strength(army)
	opposition := []*Squad{}
	if len(army) > 0 {
		if objective := nearest(army[0].Pos); objective != nil {
			for _, enemy := range enemies {
				if dist(enemy.Pos, objective.Pos) < 30 {
					opposition = append(opposition, enemy)
				}
			}
		}
	}
	if ownStrength < profile.ArmyBudget && ownStrength-strength(opposition) < profile.StopAttackRating {
		for _, squad := range army {
			computerMove(squad, state.Base, 0, 0)
		}
		return
	}
	for _, squad := range army {
		if len(army) < profile.MinAttackSquads {
			computerMove(squad, protocol.Vec3{X: state.Base.X + 14, Z: state.Base.Z}, 0, 0)
			continue
		}
		if enemy := nearest(squad.Pos); enemy != nil {
			computerMove(squad, enemy.Pos, 0, enemy.ID)
			continue
		}
		// No enemy squads left: raze the enemy base, HQ first, then the
		// closest remaining building or structure.
		siegeID := -1
		var siegePos protocol.Vec3
		siegeHQ := false
		considerSiege := func(id int, pos protocol.Vec3, team, buildingType string) {
			if team == player.Team {
				return
			}
			isHQ := buildingType == "hq"
			if siegeID < 0 || (isHQ && !siegeHQ) || (isHQ == siegeHQ && dist(squad.Pos, pos) < dist(squad.Pos, siegePos)) {
				siegeID, siegePos, siegeHQ = id, pos, isHQ
			}
		}
		for _, id := range sortedEntityIDs(world.Buildings) {
			building := world.Buildings[id]
			considerSiege(id, building.Pos, building.Team, building.Type)
		}
		for _, id := range sortedEntityIDs(world.Structures) {
			structure := world.Structures[id]
			considerSiege(id, structure.Pos, structure.Team, structure.Type)
		}
		if siegeID >= 0 {
			computerMove(squad, siegePos, 0, 0)
			siege := siegeID
			squad.ForcedBuilding = &siege
		}
	}
}
