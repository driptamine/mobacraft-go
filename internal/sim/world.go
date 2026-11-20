// Package sim is the authoritative RTS simulation: one World per room.
// It owns squads, builders, buildings, points and resources, advances
// movement/combat/economy/capture/production on Tick, and renders both
// the legacy Snapshot/Delta views and the skirmish schema-2 view.
package sim

import (
	"grimcraft-go/internal/balance"
	"grimcraft-go/internal/botai"
	"math"
	"reflect"
	"sync"

	"grimcraft-go/internal/protocol"
)

// Tuning.
const (
	DefaultSquadSpeed   = 6.0
	DefaultAttackRange  = 18.0
	DefaultDetectRange  = 30.0
	BlueBaseX           = -40.0
	RedBaseX            = 40.0
	ArriveEpsilon       = 0.5
	MeleeRange          = 3.0
	RangedDamagePerTick = 2.0 // at 10 ticks/s
	CaptureRadius       = 4.5
	CaptureRate         = 0.2 // progress per second per squad
	EnergyPerThermo     = 1.0 // energy per second per generator
	MaxRequisition      = 10000.0
	MaxEnergy           = 10000.0
)

var buildCosts = map[string]float64{
	"hq": 300, "hq-anim": 300, "barracks": 150, "infantry_command": 150, "squad": 100,
	"scout": 75, "cultist": 50,
	"builder": 80, "reinforce": 20, "listening_post": 60, "listeningPost": 60,
	"tau_listening_post": 60, "tau_listening_post_sp": 60,
	"tower": 120, "thermoGen": 80, "thermo_generator": 80, "towerBase": 80,
	"apc": 180, "mechanized": 180, "grenadeUpgrade": 30, "plasmaUpgrade": 20,
	"airfield": 200, "jet": 220, "gunTurret": 100, "plasma_generator": 80,
	"tactica_control": 120, "mars_pattern": 150, "turret": 100, "mines": 50,
}

func costOf(name string) float64 {
	if value, isValid := buildCosts[name]; isValid {
		return value
	}
	return 100
}

func constructTimeOf(faction, buildingType string) float64 {

	// wikiBuildTime mirrors the Dawn of War build times per faction (seconds).
	// Types missing here keep the legacy generic timings below.
	var wikiBuildTime = map[string]map[string]float64{
		"chaos": {
			"armoury": 45, "chaos_temple": 57, "desecrated_stronghold": 185,
			"land_mine": 20, "listening_post": 25, "machine_pit": 45,
			"plasma_generator": 30, "sacrificial_circle": 35, "thermo_plasma_generator": 65,
			"chaos_icon": 5, "greater_sacrificial_circle": 12,
		},
		"eldar": {
			"advanced_warp_generator": 65, "aspect_portal": 36, "land_mine": 20,
			"listening_post": 24, "soul_shrine": 100, "support_platform": 27,
			"support_portal": 60, "support_portal_ulthwe": 60, "warp_generator": 30,
			"webway_assembly": 120, "webway_gate": 22,
		},
		"ig": {
			"infantry_command": 36, "listening_post": 30, "mars_pattern": 45,
			"mechanized": 45, "plasma_generator": 30, "tactica_control": 45,
			"turret": 48, "mines": 20, "thermo_generator": 6,
		},
		"necrons": {
			"energy_core": 50, "forbidden_archive": 45, "greater_summoning_core": 90,
			"listening_post": 5, "monolith": 100, "obelisk": 25, "plasma_generator": 45,
			"summoning_core": 70, "summoning_core_big": 90,
			"thermoplasma_generator": 120, "turret": 45,
		},
		"orks": {
			"bigger_generator": 60, "da_boyz_hut": 40, "generator": 35,
			"land_mine": 20, "listening_post": 35, "mek_shop": 12, "pile_o_guns": 60,
			"settlement": 169, "waagh_banner": 40,
		},
		"sob": {
			"adepta_sororitas_convent": 45, "adeptus_mechanicus_workshop": 40,
			"ecclesiarchal_chapel": 150, "holy_reliquary_v2": 30, "inferno_turret": 40,
			"listening_post": 30, "plasma_generator": 30, "pristine_sanctuary": 45,
			"purgatus_mines": 20, "shrine_of_st_celestine": 8, "thermo_plasma_generator": 60,
		},
		"space marines": {
			"armoury": 45, "barracks": 45, "command_hq": 150, "land_mine": 20,
			"listening_post": 30, "machine_cult": 40, "orbital_relay": 30,
			"plasma_generator": 30, "progenitor_temple": 30, "thermo_plasma_generator": 60,
		},
		"tau": {
			"tau_barracks": 35, "tau_build_crane": 8, "tau_hq": 120, "tau_kroot_nest": 45,
			"tau_listening_post": 25, "tau_listening_post_sp": 25, "listening_post": 25,
			"tau_plasma_generator": 30, "tau_research": 100, "tau_shrine_of_kauyon": 145,
			"tau_shrine_of_mont_ka": 8, "tau_shrine_of_purpose": 50,
			"tau_thermoplasma_generator": 45, "tau_vehicle_building": 60,
		},
	}

	// Faction arrives either canonical ('space marines', 'ig', ...) or as the
	// lobby display name ('Space Marines', 'Imperial Guard', ...).
	if faction != "" {
		faction = canonicalFaction(faction)
	}
	if perFaction, found := wikiBuildTime[faction]; found {
		if buildTime, found := perFaction[buildingType]; found {
			return buildTime
		}
	}
	switch buildingType {
	case "hq", "hq-anim":
		return 15
	case "barracks", "infantry_command", "mechanized":
		return 8
	case "tower", "towerBase":
		return 7
	case "gunTurret":
		return 6
	default:
		return 5
	}
}

// Soldier is intentionally minimal: the net client only needs the slot
// array length/shape to render. Null slots mean casualties.
type Soldier struct {
	balanceCooldown  float64
	ShootTimer       float64
	MeleeTimer       float64
	HitReactionTimer float64
	HitReactionAnim  string
	ShotSerial       uint64
	MeleeSerial      uint64
	ReactionSerial   uint64
	TargetID         int

	HP    float64
	Alive bool
	// v2 soldier entity fields
	EntityID int
	FormIdx  int
	UnitType string
	MaxHP    float64
}

// Squad is the simulated unit.
type Squad struct {
	Faction        string
	Computer       bool
	ID             int
	Team           string // "Blue" | "Red"
	Owner          int
	UnitType       string
	Pos            protocol.Vec3
	Facing         protocol.Vec3
	Speed          float64
	AttackRange    float64
	DetectRange    float64
	MoveOrder      *protocol.Vec3
	Stopped        bool
	AttackMove     bool
	autoTarget     bool
	meleeOpponent  *int
	AutoChase      bool
	AttackMode     string // "ranged" | "melee"
	Stance         string // "stand" | "ranged"
	ProjectileType string
	Soldiers       []Soldier
	ForcedTarget   *int
	ForcedBuilding *int
	IdleTime       float64
	GrenadeCd      float64
	AutoReinforce  bool
	CaptureOrderSP *int
	CapturingSP    *int
	ReinforceQueue []ReinforceItem
	ReinforceProg  float64
	GrenadeQueue   []UpgradeItem
	GrenadeProg    float64
	PlasmaQueue    []UpgradeItem
	PlasmaProg     float64
	HasGrenade     bool
	HasPlasma      bool
	moved          bool // set when position changed this tick
	visualCooldown float64
	CaptureElapsed float64
	capturePoint   int
	facingChanged  bool
}

type ReinforceItem struct {
	FormIdx int
	Cost    float64
	Time    float64
}

type UpgradeItem struct {
	SoldierIdx int
	Cost       float64
	Time       float64
}

// Builder entity.
type Builder struct {
	ID           int
	Team         string
	Faction      string
	Owner        int
	Pos          protocol.Vec3
	MoveTarget   *protocol.Vec3
	BuildTarget  *int
	BuildType    string
	BuildPos     *protocol.Vec3
	BuildWorkPos *protocol.Vec3
	BuildSP      *int
	BuildProg    float64
	IsBuilding   bool
	Stance       string
	AttackMove   bool
	MeleeOrder   bool
	RepairTarget *int
	HP           float64
	MaxHP        float64
	Speed        float64
	moved        bool
}

// Building entity (hq, barracks, mechanized, ...).
type Building struct {
	HeroBase         bool
	attackCd         float64
	ID               int
	Team             string
	Faction          string
	Owner            int
	Type             string
	Pos              protocol.Vec3
	HP               float64
	MaxHP            float64
	Constructing     bool
	Progress         float64
	ConstructTime    float64
	PendingWorker    bool
	RequiresWorker   bool
	DeliveryProgress float64
	DeliveryTime     float64
	StrategicPointID *int
	Queue            []ProdItem
	Research         []ResearchItem
	Rally            *protocol.Vec3
	Addons           []string
	Tier             int
}

// Structure entity (tower, turret, thermo, plasma_generator, ...).
type Structure struct {
	ID               int
	Team             string
	Faction          string
	Owner            int
	Type             string
	Pos              protocol.Vec3
	HP               float64
	MaxHP            float64
	Constructing     bool
	Progress         float64
	ConstructTime    float64
	PendingWorker    bool
	RequiresWorker   bool
	DeliveryProgress float64
	DeliveryTime     float64
	AttackRange      float64
	AttackRate       float64
	AttackCd         float64
	Research         []ResearchItem
}

// Vehicle/APC/Airfield/Jet simplified movables.
type Vehicle struct {
	ID             int
	Kind           string // "vehicle" | "apc" | "airfield" | "jet"
	Name           string
	Model          string
	Icon           string
	ProjectileType string
	Team           string
	Faction        string
	Owner          int
	Pos            protocol.Vec3
	HP             float64
	MaxHP          float64
	MoveOrder      *protocol.Vec3
	Constructing   bool
	Progress       float64
	ConstructTime  float64
	Queue          []ProdItem
	Rally          *protocol.Vec3
	PatrolMode     string
	moved          bool
}

type ProdItem struct {
	Type     string  `json:"type"`
	Progress float64 `json:"progress"`
	Total    float64 `json:"total"`
}

type ResearchItem struct {
	Key      string
	Progress float64
	Total    float64
}

// Point is a strategic point / relic / objective.
type Point struct {
	ID                    int
	Pos                   protocol.Vec3
	Kind                  string
	Owner                 string // "" | "Blue" | "Red"
	Faction               string
	RaiseFrom             *float64
	CaptureProgress       float64 // -1..1
	HasListeningPost      bool
	ListeningPostEntityID *int
	LPTier                int
	Research              []ResearchItem
}

// Player holds per-player economy.
type Player struct {
	Computer    bool
	Difficulty  string
	ID          int
	Team        string // "Blue" | "Red"
	Faction     string
	Requisition float64
	Energy      float64
	Kills       int
	Research    map[string]bool
}

// World is a single room's game state.
type World struct {
	dota            *dotaRound
	navigation      navigationState
	computerPlayers map[int]*computerPlayer
	mapBounds       float64
	balanceMod      *balance.Mod
	mutex           sync.Mutex
	Tick            uint64
	Squads          map[int]*Squad
	Builders        map[int]*Builder
	Buildings       map[int]*Building
	Structures      map[int]*Structure
	Vehicles        map[int]*Vehicle
	Points          map[int]*Point
	Players         map[int]*Player
	Winner          string
	nextID          int
	pointsSeeded    bool
}

// NewWorld returns an empty world.
func NewWorld() *World {
	return &World{
		Squads: map[int]*Squad{}, Builders: map[int]*Builder{},
		Buildings: map[int]*Building{}, Structures: map[int]*Structure{},
		Vehicles: map[int]*Vehicle{}, Points: map[int]*Point{},
		Players: map[int]*Player{}, nextID: 1,
	}
}

func (world *World) allocID() int {
	entityID := world.nextID
	world.nextID++
	return entityID
}

func (world *World) playerOf(owner int) *Player {
	if player, isValid := world.Players[owner]; isValid {
		return player
	}
	return nil
}

// EnsurePlayer creates or returns the economy record for owner.
func (world *World) EnsurePlayer(owner int, team, faction string) *Player {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	return world.ensurePlayerLocked(owner, team, faction)
}

func (world *World) ensurePlayerLocked(owner int, team, faction string) *Player {
	if player, isValid := world.Players[owner]; isValid {
		if faction != "" {
			player.Faction = faction
		}
		if team == "Red" || team == "Blue" {
			player.Team = team
		}
		return player
	}
	if team != "Red" {
		team = "Blue"
	}
	startingRequisition, startingEnergy := initialResources(faction)
	currentPlayer := &Player{ID: owner, Team: team, Faction: faction, Requisition: startingRequisition, Energy: startingEnergy, Research: map[string]bool{}}
	world.Players[owner] = currentPlayer
	return currentPlayer
}

// EnsureMapPoints creates the selected map's strategic points and relics once.
func (world *World) EnsureMapPoints(mapName string) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	world.seedMapPointsLocked(layoutFor(mapName))
}
func (world *World) ensurePointsLocked() { world.seedMapPointsLocked(layoutFor("Arena")) }

// SpawnSquad creates a 5-soldier squad for owner at the team's base.
func (world *World) SpawnSquad(team string, owner int) *Squad {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	var baseX float64
	if team == "Red" {
		baseX = RedBaseX
	} else {
		team = "Blue"
		baseX = BlueBaseX
	}
	player := world.ensurePlayerLocked(owner, team, "")
	squadID := world.allocID()
	squad := &Squad{
		ID: squadID, Team: team, Owner: owner, Faction: player.Faction,
		Pos:            protocol.Vec3{X: baseX, Y: 0, Z: float64((squadID%5)*6 - 12)},
		Facing:         protocol.Vec3{X: 1, Y: 0, Z: 0},
		Speed:          squadMovementSpeed(player.Faction),
		AttackRange:    DefaultAttackRange,
		DetectRange:    DefaultDetectRange,
		Stopped:        true,
		AttackMode:     "ranged",
		Stance:         "ranged",
		ProjectileType: "Bullet",
		Soldiers:       []Soldier{{HP: 100, Alive: true}, {HP: 100, Alive: true}, {HP: 100, Alive: true}, {HP: 100, Alive: true}, {HP: 100, Alive: true}},
	}
	if team == "Red" {
		squad.Facing.X = -1
	}
	for index := range squad.Soldiers {
		squad.Soldiers[index].MaxHP = 100
		squad.Soldiers[index].FormIdx = index + 1
		squad.Soldiers[index].EntityID = world.allocID()
	}
	world.Squads[squadID] = squad
	return squad
}

// SpawnBuilder creates a builder unit.
func (world *World) SpawnBuilder(team string, owner int, faction string, positionX, positionZ float64) *Builder {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	if team != "Red" {
		team = "Blue"
	}
	world.ensurePlayerLocked(owner, team, faction)
	entityID := world.allocID()
	builder := &Builder{ID: entityID, Team: team, Faction: faction, Owner: owner,
		Pos: protocol.Vec3{X: positionX, Z: positionZ}, HP: 60, MaxHP: 60, Speed: 7, Stance: "stand"}
	world.Builders[entityID] = builder
	return builder
}

// RemovePlayerSquads drops every squad owned by playerID (on disconnect).
func (world *World) RemovePlayerSquads(playerID int) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	for squadID, squad := range world.Squads {
		if squad.Owner == playerID {
			delete(world.Squads, squadID)
		}
	}
	for builderID, builder := range world.Builders {
		if builder.Owner == playerID {
			delete(world.Builders, builderID)
		}
	}
	for buildingID, building := range world.Buildings {
		if building.Owner == playerID {
			world.removeListeningPostLinkLocked(building)
			delete(world.Buildings, buildingID)
		}
	}
	for structureID, structure := range world.Structures {
		if structure.Owner == playerID {
			delete(world.Structures, structureID)
		}
	}
	for vehicleID, vehicle := range world.Vehicles {
		if vehicle.Owner == playerID {
			delete(world.Vehicles, vehicleID)
		}
	}
	delete(world.Players, playerID)
}

func dist(startPosition, targetPosition protocol.Vec3) float64 {
	deltaX := startPosition.X - targetPosition.X
	deltaZ := startPosition.Z - targetPosition.Z
	return math.Sqrt(deltaX*deltaX + deltaZ*deltaZ)
}

// MuLockTestKillSoldier kills one alive soldier for tests.
func (world *World) MuLockTestKillSoldier(squadID int) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	if squad, isValid := world.Squads[squadID]; isValid {
		for index := range squad.Soldiers {
			if squad.Soldiers[index].Alive {
				squad.Soldiers[index].Alive = false
				squad.Soldiers[index].HP = 0
				return
			}
		}
	}
}

// PlaceBuilding deducts cost and creates a constructing building/structure.
func (world *World) PlaceBuilding(owner int, builderID int, buildingType string, position protocol.Vec3) (int, bool) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	return world.placeBuildingLocked(owner, builderID, buildingType, position)
}

func (world *World) placeBuildingLocked(owner int, builderID int, buildingType string, position protocol.Vec3) (int, bool) {
	player := world.playerOf(owner)
	if player == nil {
		return 0, false
	}
	var listeningPoint *Point
	if botai.IsListeningPost(buildingType) {
		for _, point := range world.Points {
			if point.Owner == player.Team && dist(point.Pos, position) < 2 && !point.HasListeningPost && point.ListeningPostEntityID == nil {
				listeningPoint = point
				break
			}
		}
		if listeningPoint == nil {
			return 0, false
		}
	}
	cost := costOf(buildingType)
	if player.Requisition < cost {
		return 0, false
	}
	player.Requisition -= cost
	entityID := world.allocID()
	switch {
	case buildingType == "tower" || buildingType == "turret" || buildingType == "towerBase" || buildingType == "gunTurret" || botai.IsGenerator(buildingType) || buildingType == "tactica_control" || buildingType == "mines":
		attackRange, rate := 0.0, 0.0
		if buildingType == "tower" || buildingType == "turret" {
			attackRange, rate = 28, 0.8
		} else if buildingType == "towerBase" {
			attackRange, rate = 32, 1.2
		} else if buildingType == "gunTurret" {
			attackRange, rate = 22, 2.5
		}
		world.Structures[entityID] = &Structure{ID: entityID, Team: player.Team, Faction: player.Faction, Owner: owner,
			Type: buildingType, Pos: position, HP: 300, MaxHP: 300, Constructing: true, RequiresWorker: true,
			ConstructTime: constructTimeOf(player.Faction, buildingType), AttackRange: attackRange, AttackRate: rate}
		if structure := world.Structures[entityID]; hasAnimatedConstruction(structure.Faction, structure.Type) {
			structure.DeliveryTime = constructionDeliveryTime
		}
	default:
		healthPoints := 400.0
		if buildingType == "hq" || buildingType == "hq-anim" {
			healthPoints = 2000
		} else if buildingType == "mechanized" {
			healthPoints = 800
		}
		constructionTime := constructTimeOf(player.Faction, buildingType)
		isConstructing := constructionTime > 0
		world.Buildings[entityID] = &Building{ID: entityID, Team: player.Team, Faction: player.Faction, Owner: owner,
			Type: buildingType, Pos: position, HP: healthPoints, MaxHP: healthPoints, Constructing: isConstructing, RequiresWorker: true,
			ConstructTime: constructionTime, Rally: &protocol.Vec3{X: position.X, Z: position.Z + 10}, Tier: 1}
		if building := world.Buildings[entityID]; building != nil && hasAnimatedConstruction(building.Faction, building.Type) {
			building.DeliveryTime = constructionDeliveryTime
		}
	}
	if listeningPoint != nil {
		world.Buildings[entityID].StrategicPointID = &listeningPoint.ID
		listeningPoint.ListeningPostEntityID = &entityID
	}
	if builder, isValid := world.Builders[builderID]; isValid && builder.Owner == owner {
		builder.BuildTarget = &entityID
		builder.BuildType = buildingType
		builder.BuildPos = &position
		builder.IsBuilding = true
		if building := world.Buildings[entityID]; building != nil && building.RequiresWorker {
			assignConstructionBuilder(builder, building.Pos, building.Faction, building.Type)
			building.PendingWorker = true
		}
		if structure := world.Structures[entityID]; structure != nil && structure.RequiresWorker {
			assignConstructionBuilder(builder, structure.Pos, structure.Faction, structure.Type)
			structure.PendingWorker = true
		}
	}
	return entityID, true
}

// TrainUnit queues production when affordable.
func (world *World) TrainUnit(owner int, buildingID int, unitType string) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	return world.trainUnitLocked(owner, buildingID, unitType)
}

func (world *World) trainUnitLocked(owner int, buildingID int, unitType string) bool {
	player := world.playerOf(owner)
	if player == nil {
		return false
	}
	cost, total := unitCostTime(player, unitType)
	if player.Requisition < cost {
		return false
	}
	if building, isValid := world.Buildings[buildingID]; isValid {
		if building.Owner != owner || building.Constructing || len(building.Queue) >= 6 {
			return false
		}
		if player.Faction == "ig" && (building.Type == "mechanized" || building.Type == "mars_pattern") {
			entry, found := imperialGuardVehicleProduction[unitType]
			if !found || entry.buildingType != building.Type || (entry.requiredTech != "" && !player.Research[entry.requiredTech]) {
				return false
			}
		}
		player.Requisition -= cost
		building.Queue = append(building.Queue, ProdItem{Type: unitType, Total: total})
		return true
	}
	if vehicle, currentIsValid := world.Vehicles[buildingID]; currentIsValid {
		if vehicle.Owner != owner || vehicle.Kind != "airfield" || len(vehicle.Queue) >= 4 {
			return false
		}
		player.Requisition -= cost
		vehicle.Queue = append(vehicle.Queue, ProdItem{Type: unitType, Total: total})
		return true
	}
	return false
}

func unitCostTime(player *Player, unitType string) (float64, float64) {
	if entry, found := imperialGuardVehicleProduction[unitType]; found {
		return entry.cost, entry.time
	}
	switch unitType {
	case "builder":
		return 80, 2
	case "jet":
		return 220, 12
	case "scout":
		return 75, 12
	case "cultist":
		return 50, 12.5
	case "squad":
		// Wiki squad build times; other races keep the generic 3s.
		if player != nil && (player.Faction == "space marines" || player.Faction == "chaos") {
			return 100, 26.4
		}
		return 100, 3
	default:
		if unitCost, isValid := buildCosts[unitType]; isValid {
			return unitCost, 3
		}
		return 100, 3
	}
}

// QueueReinforceAt queues one replacement soldier with cost/time.
func (world *World) QueueReinforceAt(owner int, squadID int, formationIndex int) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	return world.queueReinforceAtLocked(owner, squadID, formationIndex)
}

func (world *World) queueReinforceAtLocked(owner int, squadID int, formationIndex int) bool {
	squad, isValid := world.Squads[squadID]
	if !isValid || squad.Owner != owner {
		return false
	}
	player := world.playerOf(owner)
	if player == nil {
		return false
	}
	cost := 20.0
	if player.Requisition < cost {
		return false
	}
	occupied := map[int]bool{}
	for _, soldier := range squad.Soldiers {
		if soldier.Alive {
			occupied[soldier.FormIdx] = true
		}
	}
	for _, reinforcementItem := range squad.ReinforceQueue {
		occupied[reinforcementItem.FormIdx] = true
	}
	if formationIndex <= 0 {
		formationIndex = -1
		for index := 1; index <= 13; index++ {
			if !occupied[index] {
				formationIndex = index
				break
			}
		}
		if formationIndex < 0 {
			return false
		}
	} else if occupied[formationIndex] {
		return false
	}
	player.Requisition -= cost
	squad.ReinforceQueue = append(squad.ReinforceQueue, ReinforceItem{FormIdx: formationIndex, Cost: cost, Time: 7})
	return true
}

// QueueResearch queues research on a building/structure/point.
func (world *World) QueueResearch(owner int, entityID int, key string) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	player := world.playerOf(owner)
	if player == nil || key == "" || player.Research[key] {
		return false
	}
	cost := 50.0
	if player.Requisition < cost {
		return false
	}
	researchItem := ResearchItem{Key: key, Total: 10}
	if building, buildingFound := world.Buildings[entityID]; buildingFound {
		if building.Owner != owner {
			return false
		}
		player.Requisition -= cost
		building.Research = append(building.Research, researchItem)
		return true
	}
	if structure, currentIsValid := world.Structures[entityID]; currentIsValid {
		if structure.Owner != owner {
			return false
		}
		player.Requisition -= cost
		structure.Research = append(structure.Research, researchItem)
		return true
	}
	if point, pointFound := world.Points[entityID]; pointFound {
		player.Requisition -= cost
		point.Research = append(point.Research, researchItem)
		return true
	}
	// generic research on player
	player.Requisition -= cost
	player.Research[key+"_queued"] = false
	// store as pending via a generic building-less queue: use first owned building
	for _, currentBuilding := range world.Buildings {
		if currentBuilding.Owner == owner {
			currentBuilding.Research = append(currentBuilding.Research, researchItem)
			return true
		}
	}
	// no building: complete after time via player queue emulation
	player.Research[key] = false
	return true
}

// UpgradePoint researches listening-post tiers.
func (world *World) UpgradePoint(owner int, pointID int, key string) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	point, isValid := world.Points[pointID]
	if !isValid {
		return false
	}
	player := world.playerOf(owner)
	if player == nil || player.Requisition < 50 {
		return false
	}
	player.Requisition -= 50
	point.Research = append(point.Research, ResearchItem{Key: key, Total: 8})
	return true
}

// Demolish removes an owned entity.
func (world *World) Demolish(owner int, entityID int) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	if building, buildingFound := world.Buildings[entityID]; buildingFound {
		if building.Owner != owner {
			return false
		}
		world.removeListeningPostLinkLocked(building)
		delete(world.Buildings, entityID)
		return true
	}
	if structure, structureFound := world.Structures[entityID]; structureFound {
		if structure.Owner != owner {
			return false
		}
		delete(world.Structures, entityID)
		return true
	}
	if vehicle, vehicleFound := world.Vehicles[entityID]; vehicleFound {
		if vehicle.Owner != owner {
			return false
		}
		delete(world.Vehicles, entityID)
		return true
	}
	return false
}

// --- Advance: movement, combat, economy, production, capture, win ---

// Advance moves the simulation forward by dt seconds.
func (world *World) Advance(deltaTime float64) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	world.Tick++
	world.tickActionTimersLocked(deltaTime)
	world.updateMeleeContactsLocked()
	world.ensurePointsLocked()
	if world.dota == nil {
		world.advanceComputersLocked(deltaTime)
	} else {
		world.advanceDotaLocked(deltaTime)
	}
	world.refreshNavigationLocked()
	// income
	for _, player := range world.Players {
		player.Requisition = math.Min(MaxRequisition, player.Requisition+world.requisitionRateLocked(player)/resourceRateSeconds*deltaTime)
		player.Energy = math.Min(MaxEnergy, player.Energy+world.powerRateLocked(player)/resourceRateSeconds*deltaTime)
	}
	// builders movement + build progress
	for _, builder := range world.Builders {
		builder.moved = false
		world.resolveBuilderFootprintsLocked(builder)
		if world.advanceConstructionBuilderLocked(builder, deltaTime) {
			continue
		}
		if builder.IsBuilding {
			builder.BuildProg += deltaTime
			total := 3.0
			if builder.BuildSP != nil {
				total = 2
			}
			if builder.BuildProg >= total {
				world.finishBuilderLocked(builder)
			}
			continue
		}
		if builder.MoveTarget != nil {
			target := *builder.MoveTarget
			target, destination := world.groundWaypointLocked(builder.ID, builder.Pos, target, .5)
			builder.MoveTarget = &destination

			distance := dist(builder.Pos, target)
			step := builder.Speed * deltaTime
			if step <= 0 {
				step = 7 * deltaTime
			}
			if distance <= math.Max(step, 0.3) {
				builder.Pos.X, builder.Pos.Z = target.X, target.Z
				if dist(target, destination) < .01 {
					builder.MoveTarget = nil
				}
				builder.moved = true
			} else {
				directionX := (target.X - builder.Pos.X) / distance
				directionZ := (target.Z - builder.Pos.Z) / distance
				builder.Pos.X += directionX * step
				builder.Pos.Z += directionZ * step
				builder.moved = true
			}
		}
		world.resolveBuilderFootprintsLocked(builder)
	}
	// buildings construction + production + research
	for _, building := range world.Buildings {
		if building.Constructing {
			if building.RequiresWorker || hasAnimatedConstruction(building.Faction, building.Type) {
				building.PendingWorker = !world.hasActiveConstructionWorkerLocked(building.ID, building.Owner, building.Pos, building.Faction, building.Type)
			}
			constructionDelta := deltaTime
			if building.DeliveryTime > 0 {
				deliveryDelta := math.Min(deltaTime, math.Max(0, building.DeliveryTime-building.DeliveryProgress))
				building.DeliveryProgress += deliveryDelta
				constructionDelta -= deliveryDelta
			}
			if building.PendingWorker {
				continue
			}
			if building.ConstructTime <= 0 {
				building.Constructing = false
			} else {
				building.Progress += constructionDelta
				if building.Progress >= building.ConstructTime {
					building.Constructing = false
				}
			}
			if !building.Constructing {
				world.finishConstructionBuildersLocked(building.ID)
				if building.StrategicPointID != nil {
					if point := world.Points[*building.StrategicPointID]; point != nil {
						point.HasListeningPost = true
						point.LPTier = 1
						point.Faction = building.Faction
					}
				}
			}
			continue
		}
		if len(building.Research) > 0 {
			building.Research[0].Progress += deltaTime
			if building.Research[0].Progress >= building.Research[0].Total {
				key := building.Research[0].Key
				building.Research = building.Research[1:]
				if currentPlayer := world.playerOf(building.Owner); currentPlayer != nil {
					delete(currentPlayer.Research, key+"_queued")
					currentPlayer.Research[key] = true
				}
			}
		}
		if len(building.Queue) > 0 {
			building.Queue[0].Progress += deltaTime
			if building.Queue[0].Progress >= building.Queue[0].Total {
				item := building.Queue[0]
				building.Queue = building.Queue[1:]
				world.spawnProductionLocked(building.Team, building.Owner, item.Type, building.Pos, building.Rally, building.Faction, building.Type)
			}
		}
		// Fortified listening posts (tier 2+) fire their mounted bolters.
		if !building.Constructing && !building.PendingWorker && botai.IsListeningPost(building.Type) && building.StrategicPointID != nil {
			if point := world.Points[*building.StrategicPointID]; point != nil && point.LPTier >= 2 {
				building.attackCd = math.Max(0, building.attackCd-deltaTime)
				if building.attackCd <= 0 {
					if targetID, isValid := world.closestEnemySoldier(building.Pos, building.Team, 28); isValid {
						world.damageSoldier(targetID, 12, building.Owner)
						building.attackCd = 1
					}
				}
			}
		}
	}
	for _, producingStructure := range world.Structures {
		if producingStructure.Constructing {
			if producingStructure.RequiresWorker || hasAnimatedConstruction(producingStructure.Faction, producingStructure.Type) {
				producingStructure.PendingWorker = !world.hasActiveConstructionWorkerLocked(producingStructure.ID, producingStructure.Owner, producingStructure.Pos, producingStructure.Faction, producingStructure.Type)
			}
			constructionDelta := deltaTime
			if producingStructure.DeliveryTime > 0 {
				deliveryDelta := math.Min(deltaTime, math.Max(0, producingStructure.DeliveryTime-producingStructure.DeliveryProgress))
				producingStructure.DeliveryProgress += deliveryDelta
				constructionDelta -= deliveryDelta
			}
			if producingStructure.PendingWorker {
				continue
			}
			producingStructure.Progress += constructionDelta
			if producingStructure.Progress >= producingStructure.ConstructTime {
				producingStructure.Constructing = false
				world.finishConstructionBuildersLocked(producingStructure.ID)
			}
			continue
		}
		if len(producingStructure.Research) > 0 {
			producingStructure.Research[0].Progress += deltaTime
			if producingStructure.Research[0].Progress >= producingStructure.Research[0].Total {
				key := producingStructure.Research[0].Key
				producingStructure.Research = producingStructure.Research[1:]
				if generatorOwner := world.playerOf(producingStructure.Owner); generatorOwner != nil {
					generatorOwner.Research[key] = true
				}
			}
		}
		// turret defense
		if producingStructure.AttackRange > 0 {
			producingStructure.AttackCd = math.Max(0, producingStructure.AttackCd-deltaTime)
			if producingStructure.AttackCd <= 0 {
				if targetID, isValid := world.closestEnemySoldier(producingStructure.Pos, producingStructure.Team, producingStructure.AttackRange); isValid {
					world.damageSoldier(targetID, 12, producingStructure.Owner)
					producingStructure.AttackCd = 1
					if producingStructure.AttackRate > 0 {
						producingStructure.AttackCd = 1 / producingStructure.AttackRate
					}
				}
			}
		}
	}
	world.refreshNavigationLocked()
	for _, vehicle := range world.Vehicles {
		if vehicle.Constructing {
			vehicle.Progress += deltaTime
			if vehicle.Progress >= vehicle.ConstructTime {
				vehicle.Constructing = false
			}
			continue
		}
		if len(vehicle.Queue) > 0 {
			vehicle.Queue[0].Progress += deltaTime
			if vehicle.Queue[0].Progress >= vehicle.Queue[0].Total {
				item := vehicle.Queue[0]
				vehicle.Queue = vehicle.Queue[1:]
				world.spawnProductionLocked(vehicle.Team, vehicle.Owner, item.Type, vehicle.Pos, vehicle.Rally, vehicle.Faction, vehicle.Kind)
			}
		}
		if vehicle.MoveOrder != nil {
			target := *vehicle.MoveOrder
			destination := target
			grounded := vehicle.Kind != "jet" && vehicle.Kind != "airfield"
			radius := 1.0
			if vehicle.Kind == "apc" {
				radius = 1.2
			}
			if grounded {
				target, destination = world.groundWaypointLocked(vehicle.ID, vehicle.Pos, target, radius)
				vehicle.MoveOrder = &destination
			}
			previous := vehicle.Pos
			if grounded {
				vehicle.Pos = world.groundStepLocked(vehicle.Pos, target, 8*deltaTime, radius)
			} else {
				distance := dist(vehicle.Pos, target)
				if distance > 0 {
					fraction := math.Min(1, 8*deltaTime/distance)
					vehicle.Pos.X += (target.X - vehicle.Pos.X) * fraction
					vehicle.Pos.Z += (target.Z - vehicle.Pos.Z) * fraction
				}
			}
			vehicle.moved = dist(previous, vehicle.Pos) > 1e-9
			if dist(vehicle.Pos, destination) <= .3 {
				vehicle.MoveOrder = nil
			}
		}
	}
	// squads
	for _, squad := range world.Squads {
		world.acquireTargetLocked(squad)
		squad.moved = false
		squad.facingChanged = false
		if squad.GrenadeCd > 0 {
			squad.GrenadeCd = math.Max(0, squad.GrenadeCd-deltaTime)
		}
		// auto-reinforce: queue one missing soldier when affordable
		if squad.AutoReinforce && len(squad.ReinforceQueue) == 0 {
			if reinforcementOwner := world.playerOf(squad.Owner); reinforcementOwner != nil && reinforcementOwner.Requisition >= 20 {
				occupied := map[int]bool{}
				for _, currentSoldier := range squad.Soldiers {
					if currentSoldier.Alive {
						occupied[currentSoldier.FormIdx] = true
					}
				}
				for index := 1; index <= 13; index++ {
					if !occupied[index] {
						reinforcementOwner.Requisition -= 20
						squad.ReinforceQueue = append(squad.ReinforceQueue, ReinforceItem{FormIdx: index, Cost: 20, Time: 7})
						break
					}
				}
			}
		}
		// reinforce queue
		if len(squad.ReinforceQueue) > 0 {
			squad.ReinforceProg += deltaTime
			if squad.ReinforceProg >= squad.ReinforceQueue[0].Time {
				reinforcementItem := squad.ReinforceQueue[0]
				squad.ReinforceQueue = squad.ReinforceQueue[1:]
				squad.ReinforceProg = 0
				filled := false
				for currentIndex := range squad.Soldiers {
					if !squad.Soldiers[currentIndex].Alive && squad.Soldiers[currentIndex].FormIdx == reinforcementItem.FormIdx {
						squad.Soldiers[currentIndex].Alive = true
						squad.Soldiers[currentIndex].HP = 100
						filled = true
						break
					}
				}
				if !filled && len(squad.Soldiers) < 13 {
					squad.Soldiers = append(squad.Soldiers, Soldier{HP: 100, Alive: true, FormIdx: reinforcementItem.FormIdx, MaxHP: 100, EntityID: world.allocID()})
				}
			}
		}
		// upgrade queues (grenade/plasma): timed then flag
		if len(squad.GrenadeQueue) > 0 {
			squad.GrenadeProg += deltaTime
			if squad.GrenadeProg >= squad.GrenadeQueue[0].Time {
				squad.GrenadeQueue = squad.GrenadeQueue[1:]
				squad.GrenadeProg = 0
				squad.HasGrenade = true
			}
		}
		if len(squad.PlasmaQueue) > 0 {
			squad.PlasmaProg += deltaTime
			if squad.PlasmaProg >= squad.PlasmaQueue[0].Time {
				squad.PlasmaQueue = squad.PlasmaQueue[1:]
				squad.PlasmaProg = 0
				squad.HasPlasma = true
			}
		}
		// Movement toward move order.
		if squad.MoveOrder != nil && !squad.Stopped && !(squad.AttackMove && squad.ForcedTarget != nil) {
			target, destination := world.groundWaypointLocked(squad.ID, squad.Pos, *squad.MoveOrder, .5)
			squad.MoveOrder = &destination
			previous := squad.Pos
			squad.Pos = world.groundStepLocked(squad.Pos, target, squad.Speed*deltaTime, .5)
			moved := dist(previous, squad.Pos)
			if moved > 1e-9 {
				directionX, directionZ := (squad.Pos.X-previous.X)/moved, (squad.Pos.Z-previous.Z)/moved
				if math.Abs(squad.Facing.X-directionX) > 1e-6 || math.Abs(squad.Facing.Z-directionZ) > 1e-6 {
					squad.Facing.X, squad.Facing.Z = directionX, directionZ
					squad.facingChanged = true
				}
				squad.moved = true
				squad.IdleTime = 0
			}
			if dist(squad.Pos, destination) <= ArriveEpsilon {
				squad.Pos = world.groundStepLocked(squad.Pos, destination, ArriveEpsilon, .5)
				squad.MoveOrder = nil
				squad.Stopped = true
			}

		} else {
			squad.IdleTime += deltaTime
		}
		// capture progress
		if squad.CaptureOrderSP != nil {
			if currentPoint, currentIsValid := world.Points[*squad.CaptureOrderSP]; currentIsValid {
				if dist(squad.Pos, currentPoint.Pos) <= CaptureRadius+1 {
					if squad.capturePoint != currentPoint.ID {
						squad.capturePoint = currentPoint.ID
						squad.CaptureElapsed = 0
					}
					squad.CaptureElapsed += deltaTime
					delta := CaptureRate * deltaTime
					if squad.CaptureElapsed < captureRodPlacementTime {
						delta = 0
					}
					if squad.CaptureElapsed >= captureRodPlacementTime && currentPoint.RaiseFrom == nil {
						value := math.Abs(currentPoint.CaptureProgress)
						currentPoint.RaiseFrom = &value
					}
					if squad.Team == "Blue" {
						currentPoint.CaptureProgress = math.Min(1, currentPoint.CaptureProgress+delta)
					} else {
						currentPoint.CaptureProgress = math.Max(-1, currentPoint.CaptureProgress-delta)
					}
					squad.CapturingSP = squad.CaptureOrderSP
					world.updatePointOwner(currentPoint)
					if currentPoint.Owner == squad.Team {
						squad.CapturingSP = nil
						squad.CaptureOrderSP = nil
					}
				} else {
					squad.CapturingSP = nil
				}
			} else {
				squad.CaptureOrderSP = nil
			}
		}
		// Movement, damage and animation events come from the same attack decision.
		combatTarget := squad.ForcedTarget
		if squad.meleeOpponent != nil {
			combatTarget = squad.meleeOpponent
		}
		if combatTarget != nil {
			target, targetFound := world.Squads[*combatTarget]
			if !targetFound || target.Team == squad.Team {
				squad.ForcedTarget = nil
			} else {
				distance := dist(squad.Pos, target.Pos)
				melee := squad.AttackMode == "melee" || squad.meleeOpponent != nil
				stopRange := squad.AttackRange
				if melee {
					stopRange = MeleeRange
				}
				if distance > stopRange && distance > 0 && (squad.MoveOrder == nil || squad.AttackMove) {
					step := math.Min(squad.Speed*deltaTime, distance-stopRange*.85)
					waypoint, _ := world.groundWaypointLocked(squad.ID, squad.Pos, target.Pos, .5)
					previous := squad.Pos
					squad.Pos = world.groundStepLocked(squad.Pos, waypoint, step, .5)
					if moved := dist(previous, squad.Pos); moved > 1e-9 {
						squad.Facing.X = (squad.Pos.X - previous.X) / moved
						squad.Facing.Z = (squad.Pos.Z - previous.Z) / moved
						squad.moved = true
						squad.facingChanged = true
					}
				}
				distance = dist(squad.Pos, target.Pos)
				if distance <= squad.AttackRange {
					targetDirectionX, targetDirectionZ := target.Pos.X-squad.Pos.X, target.Pos.Z-squad.Pos.Z
					if length := math.Hypot(targetDirectionX, targetDirectionZ); length > 0 {
						squad.Facing.X = targetDirectionX / length
						squad.Facing.Z = targetDirectionZ / length
					}
					if distance <= MeleeRange && (melee || target.meleeOpponent != nil) {
						world.enterMeleeLocked(squad, target)
						world.enterMeleeLocked(target, squad)
					}
					closeCombat := squad.meleeOpponent != nil && *squad.meleeOpponent == target.ID && distance <= meleeReleaseRange
					if !closeCombat && world.balanceRangedAttack(squad, target) {
						continue
					}
					for victimIndex := range target.Soldiers {
						victim := &target.Soldiers[victimIndex]
						if !victim.Alive {
							continue
						}
						world.recordAttackLocked(squad, target, victim, closeCombat)
						damage := RangedDamagePerTick
						if closeCombat {
							damage = 4
						}
						victim.HP -= damage
						if victim.HP <= 0 {
							victim.Alive = false
							if killer := world.playerOf(squad.Owner); killer != nil {
								killer.Kills++
							}
						}
						break
					}
				}
			}
		}
		if squad.ForcedBuilding != nil {
			building, buildingFound := world.Buildings[*squad.ForcedBuilding]
			structure, structureFound := world.Structures[*squad.ForcedBuilding]
			if !buildingFound && !structureFound {
				squad.ForcedBuilding = nil
			} else {
				var targetPos protocol.Vec3
				var targetTeam string
				if buildingFound {
					targetPos, targetTeam = building.Pos, building.Team
				} else {
					targetPos, targetTeam = structure.Pos, structure.Team
				}
				if targetTeam == squad.Team {
					squad.ForcedBuilding = nil
				} else {
					stopRange := squad.AttackRange + 4
					distance := dist(squad.Pos, targetPos)
					if distance > stopRange && distance > 0 && (squad.MoveOrder == nil || squad.AttackMove) {
						step := math.Min(squad.Speed*deltaTime, distance-stopRange*.85)
						waypoint, _ := world.groundWaypointLocked(squad.ID, squad.Pos, targetPos, .5)
						previous := squad.Pos
						squad.Pos = world.groundStepLocked(squad.Pos, waypoint, step, .5)
						if moved := dist(previous, squad.Pos); moved > 1e-9 {
							squad.Facing.X = (squad.Pos.X - previous.X) / moved
							squad.Facing.Z = (squad.Pos.Z - previous.Z) / moved
							squad.moved = true
							squad.facingChanged = true
						}
					}
					distance = dist(squad.Pos, targetPos)
					if distance <= squad.AttackRange+4 {
						if length := math.Hypot(targetPos.X-squad.Pos.X, targetPos.Z-squad.Pos.Z); length > 0 {
							squad.Facing.X = (targetPos.X - squad.Pos.X) / length
							squad.Facing.Z = (targetPos.Z - squad.Pos.Z) / length
							squad.facingChanged = true
						}
						world.damageBuildingLocked(*squad.ForcedBuilding, RangedDamagePerTick, squad.Owner)
					}
				}
			}
		}
	}
	// Remove wiped squads.
	for squadID, squad := range world.Squads {
		alive := false
		for _, soldier := range squad.Soldiers {
			if soldier.Alive {
				alive = true
				break
			}
		}
		if !alive {
			delete(world.Squads, squadID)
		}
	}
	// point research (listening post tiers)
	for _, researchPoint := range world.Points {
		if len(researchPoint.Research) > 0 {
			researchPoint.Research[0].Progress += deltaTime
			if researchPoint.Research[0].Progress >= researchPoint.Research[0].Total {
				key := researchPoint.Research[0].Key
				researchPoint.Research = researchPoint.Research[1:]
				switch key {
				case "fortified_position":
					researchPoint.LPTier = 2
				case "heavily_fortified_position":
					researchPoint.LPTier = 3
				}
			}
		}
	}
	world.updateWinnerLocked()
}

func (world *World) closestEnemySoldier(position protocol.Vec3, team string, attackRange float64) (int, bool) {
	closestDistance := -1
	currentClosestDistance := attackRange
	for _, squad := range world.Squads {
		if squad.Team == team {
			continue
		}
		for index := range squad.Soldiers {
			if !squad.Soldiers[index].Alive {
				continue
			}
			distance := dist(position, squad.Pos)
			if distance < currentClosestDistance {
				currentClosestDistance = distance
				closestDistance = squad.Soldiers[index].EntityID
				if closestDistance == 0 {
					closestDistance = squad.ID*1000 + index + 1
				}
			}
		}
	}
	if closestDistance < 0 {
		return 0, false
	}
	return closestDistance, true
}

func (world *World) damageSoldier(entityID int, damage float64, killerOwner int) {
	for _, squad := range world.Squads {
		for index := range squad.Soldiers {
			soldierEntityID := squad.Soldiers[index].EntityID
			if soldierEntityID == 0 {
				soldierEntityID = squad.ID*1000 + index + 1
			}
			if soldierEntityID == entityID && squad.Soldiers[index].Alive {
				squad.Soldiers[index].HP -= damage
				if squad.Soldiers[index].HP <= 0 {
					squad.Soldiers[index].Alive = false
					if killer := world.playerOf(killerOwner); killer != nil {
						killer.Kills++
					}
				}
				return
			}
		}
	}
}

func (world *World) updatePointOwner(point *Point) {
	if point.CaptureProgress >= 1 {
		point.Owner = "Blue"
	} else if point.CaptureProgress <= -1 {
		point.Owner = "Red"
	}
}

func (world *World) finishBuilderLocked(builder *Builder) {
	defer func() {
		builder.IsBuilding = false
		builder.BuildProg = 0
		builder.BuildTarget = nil
		builder.BuildSP = nil
	}()
	if builder.BuildSP != nil {
		if point, buildingFound := world.Points[*builder.BuildSP]; buildingFound {
			if (point.Owner == "Blue" && builder.Team == "Blue") || (point.Owner == "Red" && builder.Team == "Red") {
				if !point.HasListeningPost {
					point.HasListeningPost = true
					point.LPTier = 1
					point.Faction = builder.Faction
				}
			}
		}
		return
	}
	if builder.BuildTarget != nil {
		if building, currentIsValid := world.Buildings[*builder.BuildTarget]; currentIsValid {
			building.Constructing = true
			return
		}
		if structure, structureFound := world.Structures[*builder.BuildTarget]; structureFound {
			structure.Constructing = true
			return
		}
	}
}

// Eight exit gates around a production structure (E, NE, N, NW, W, SW, S,
// SE): the rally direction snaps to the nearest one so fresh units walk out
// toward the rally point instead of piling up on one fixed side.
func spawnGateOffset(deltaX, deltaZ float64) (float64, float64) {
	if deltaX == 0 && deltaZ == 0 {
		return -1, 0
	}
	dirs := [8][2]float64{{1, 0}, {math.Sqrt2 / 2, math.Sqrt2 / 2}, {0, 1}, {-math.Sqrt2 / 2, math.Sqrt2 / 2}, {-1, 0}, {-math.Sqrt2 / 2, -math.Sqrt2 / 2}, {0, -1}, {math.Sqrt2 / 2, -math.Sqrt2 / 2}}
	index := int(math.Round(math.Atan2(deltaZ, deltaX) / (math.Pi / 4)))
	index = ((index % 8) + 8) % 8
	return dirs[index][0], dirs[index][1]
}

func productionSpawnPoint(faction, buildingType string, position protocol.Vec3, rally *protocol.Vec3) protocol.Vec3 {
	bounds := structureFootprint(faction, buildingType, false, position)
	extentX := bounds.MaxX - bounds.MinX
	extentZ := bounds.MaxZ - bounds.MinZ
	radius := math.Max(math.Max(extentX, extentZ), 4)/2 + 3
	if rally == nil {
		return protocol.Vec3{X: position.X - 6, Z: position.Z}
	}
	gateX, gateZ := spawnGateOffset(rally.X-position.X, rally.Z-position.Z)
	return protocol.Vec3{X: position.X + gateX*radius, Z: position.Z + gateZ*radius}
}

func (world *World) spawnProductionLocked(team string, owner int, unitType string, targetPosition protocol.Vec3, rally *protocol.Vec3, producerFaction, producerType string) {
	spawn := productionSpawnPoint(producerFaction, producerType, targetPosition, rally)
	if entry, found := imperialGuardVehicleProduction[unitType]; found && world.playerOf(owner) != nil && world.playerOf(owner).Faction == "ig" {
		vehicleID := world.allocID()
		projectileType := "Bullet"
		if unitType == "chimera" {
			projectileType = "beam"
		}
		world.Vehicles[vehicleID] = &Vehicle{ID: vehicleID, Kind: "vehicle", Team: team, Owner: owner, Faction: "ig",
			Name: unitType, Model: "models/ig/vehicles/" + unitType + ".glb",
			Icon: "assets/icons/Imperial Guard/Units/Vehicles/" + entry.icon, ProjectileType: projectileType,
			Pos: spawn, HP: 500, MaxHP: 500, MoveOrder: rally}
		return
	}
	switch unitType {
	case "builder":
		builderID := world.allocID()
		world.Builders[builderID] = &Builder{ID: builderID, Team: team, Owner: owner, Pos: spawn,
			HP: 60, MaxHP: 60, Speed: 7, Stance: "stand", MoveTarget: rally, Faction: world.Players[owner].Faction}
	case "jet":
		jetID := world.allocID()
		position := spawn
		moveOrder := rally
		world.Vehicles[jetID] = &Vehicle{ID: jetID, Kind: "jet", Team: team, Owner: owner, Pos: position,
			HP: 200, MaxHP: 200, MoveOrder: moveOrder}
	case "apc":
		transportID := world.allocID()
		world.Vehicles[transportID] = &Vehicle{ID: transportID, Kind: "apc", Team: team, Owner: owner,
			Pos: spawn, HP: 400, MaxHP: 400, MoveOrder: rally}
	case "airfield":
		airfieldID := world.allocID()
		world.Vehicles[airfieldID] = &Vehicle{ID: airfieldID, Kind: "airfield", Team: team, Owner: owner,
			Pos: protocol.Vec3{X: targetPosition.X, Z: targetPosition.Z}, HP: 600, MaxHP: 600}
	default:
		// default: infantry squad
		player := world.ensurePlayerLocked(owner, team, "")
		squadID := world.allocID()
		squad := &Squad{ID: squadID, Team: team, Owner: owner, UnitType: unitType, Faction: player.Faction, Computer: player.Computer,
			Pos: spawn, Facing: protocol.Vec3{X: 1},
			Speed: squadMovementSpeed(player.Faction), AttackRange: DefaultAttackRange, DetectRange: DefaultDetectRange,
			Stopped: true, AttackMode: "ranged", Stance: "ranged", ProjectileType: "Bullet",
			Soldiers: []Soldier{{HP: 100, Alive: true, FormIdx: 1, MaxHP: 100}, {HP: 100, Alive: true, FormIdx: 2, MaxHP: 100}, {HP: 100, Alive: true, FormIdx: 3, MaxHP: 100}, {HP: 100, Alive: true, FormIdx: 4, MaxHP: 100}, {HP: 100, Alive: true, FormIdx: 5, MaxHP: 100}},
		}
		if team == "Red" {
			squad.Facing.X = -1
		}
		for index := range squad.Soldiers {
			squad.Soldiers[index].EntityID = world.allocID()
		}
		if rally != nil {
			currentTargetPosition := *rally
			squad.MoveOrder = &currentTargetPosition
			squad.Stopped = false
		}
		world.Squads[squadID] = squad
	}
}

func (world *World) updateWinnerLocked() {
	if world.Winner != "" {
		return
	}
	if world.dota != nil {
		world.updateDotaWinnerLocked()
		return
	}
	blueAlive, redAlive := false, false
	for _, squad := range world.Squads {
		if aliveCount(squad) == 0 {
			continue
		}
		if squad.Team == "Blue" {
			blueAlive = true
		} else {
			redAlive = true
		}
	}
	for _, building := range world.Buildings {
		if building.HP <= 0 {
			continue
		}
		if building.Team == "Blue" {
			blueAlive = true
		} else {
			redAlive = true
		}
	}
	if len(world.Squads)+len(world.Buildings)+len(world.Builders) == 0 {
		return
	}
	if !blueAlive && redAlive {
		world.Winner = "red"
	} else if !redAlive && blueAlive {
		world.Winner = "blue"
	}
}

// --- Commands (all callable with the world lock held via methods) ---

// MoveSquads orders squads owned by playerID to target. Returns rejected IDs.
func (world *World) MoveSquads(playerID int, squadIDs []int, target protocol.Vec3) (moved, rejected []int) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	for _, squadID := range squadIDs {
		if squad, found := world.Squads[squadID]; found && squad.Owner == playerID {
			order := target
			squad.MoveOrder = &order
			squad.Stopped = false
			squad.ForcedTarget = nil
			squad.ForcedBuilding = nil
			squad.CaptureOrderSP = nil
			moved = append(moved, squadID)
			continue
		}
		if builder, found := world.Builders[squadID]; found && builder.Owner == playerID {
			targetPosition := target
			builder.MoveTarget = &targetPosition
			clearBuilderBuildOrder(builder)
			moved = append(moved, squadID)
			continue
		}
		if vehicle, found := world.Vehicles[squadID]; found && vehicle.Owner == playerID {
			currentTargetPosition := target
			vehicle.MoveOrder = &currentTargetPosition
			moved = append(moved, squadID)
			continue
		}
		rejected = append(rejected, squadID)
	}
	return moved, rejected
}

// MoveUnits is the schema-2 move for squads/builders/vehicles, with optional capture point.
func (world *World) MoveUnits(playerID int, entityIDs []int, target protocol.Vec3, pointID *int) (moved, rejected []int) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	for _, entityID := range entityIDs {
		if squad, found := world.Squads[entityID]; found && squad.Owner == playerID {
			squadDestination := target
			squad.MoveOrder = &squadDestination
			squad.Stopped = false
			squad.ForcedTarget = nil
			squad.ForcedBuilding = nil
			squad.CaptureOrderSP = pointID
			squad.AttackMove = false
			squad.autoTarget = false
			moved = append(moved, entityID)
			continue
		}
		if builder, found := world.Builders[entityID]; found && builder.Owner == playerID {
			builderDestination := target
			builder.MoveTarget = &builderDestination
			clearBuilderBuildOrder(builder)
			moved = append(moved, entityID)
			continue
		}
		if vehicle, found := world.Vehicles[entityID]; found && vehicle.Owner == playerID {
			vehicleDestination := target
			vehicle.MoveOrder = &vehicleDestination
			moved = append(moved, entityID)
			continue
		}
		rejected = append(rejected, entityID)
	}
	return moved, rejected
}

// StopSquads halts squads owned by playerID.
func (world *World) StopSquads(playerID int, squadIDs []int) (stopped, rejected []int) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	for _, squadID := range squadIDs {
		if squad, found := world.Squads[squadID]; found && squad.Owner == playerID {
			squad.MoveOrder = nil
			squad.Stopped = true
			squad.AttackMove = false
			squad.AutoChase = false
			squad.ForcedTarget = nil
			squad.ForcedBuilding = nil
			squad.CaptureOrderSP = nil
			stopped = append(stopped, squadID)
			continue
		}
		rejected = append(rejected, squadID)
	}
	return stopped, rejected
}

// StopUnits halts any owned units.
func (world *World) StopUnits(playerID int, entityIDs []int) (stopped, rejected []int) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	for _, entityID := range entityIDs {
		if squad, found := world.Squads[entityID]; found && squad.Owner == playerID {
			squad.MoveOrder = nil
			squad.Stopped = true
			squad.AttackMove = false
			squad.AutoChase = false
			squad.ForcedTarget = nil
			squad.ForcedBuilding = nil
			squad.CaptureOrderSP = nil
			stopped = append(stopped, entityID)
			continue
		}
		if builder, found := world.Builders[entityID]; found && builder.Owner == playerID {
			builder.MoveTarget = nil
			clearBuilderBuildOrder(builder)
			stopped = append(stopped, entityID)
			continue
		}
		if vehicle, found := world.Vehicles[entityID]; found && vehicle.Owner == playerID {
			vehicle.MoveOrder = nil
			stopped = append(stopped, entityID)
			continue
		}
		rejected = append(rejected, entityID)
	}
	return stopped, rejected
}

// Attack orders attacker squads (owned by playerID) onto a target squad.
func (world *World) Attack(playerID int, attackerIDs []int, targetSquad int) (acceptedIDs, rejected []int) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	target, found := world.Squads[targetSquad]
	if !found {
		return nil, attackerIDs
	}
	for _, attackerID := range attackerIDs {
		squad, found := world.Squads[attackerID]
		if !found || squad.Owner != playerID || target.Team == squad.Team {
			rejected = append(rejected, attackerID)
			continue
		}
		targetID := targetSquad
		squad.ForcedTarget = &targetID
		squad.ForcedBuilding = nil
		squad.autoTarget = false
		squad.AttackMove = false
		squad.MoveOrder = nil
		squad.Stopped = false
		acceptedIDs = append(acceptedIDs, attackerID)
	}
	return acceptedIDs, rejected
}

// AttackBuilding orders squads to raze an enemy building or structure.
func (world *World) AttackBuilding(playerID int, attackerIDs []int, targetBuilding int) (acceptedIDs, rejected []int) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	building, buildingFound := world.Buildings[targetBuilding]
	structure, structureFound := world.Structures[targetBuilding]
	if !buildingFound && !structureFound {
		return nil, attackerIDs
	}
	targetTeam := ""
	if buildingFound {
		targetTeam = building.Team
	} else {
		targetTeam = structure.Team
	}
	for _, attackerID := range attackerIDs {
		squad, found := world.Squads[attackerID]
		if !found || squad.Owner != playerID || targetTeam == squad.Team {
			rejected = append(rejected, attackerID)
			continue
		}
		targetID := targetBuilding
		squad.ForcedBuilding = &targetID
		squad.ForcedTarget = nil
		squad.autoTarget = false
		squad.AttackMove = false
		squad.MoveOrder = nil
		squad.CaptureOrderSP = nil
		squad.CapturingSP = nil
		squad.Stopped = false
		acceptedIDs = append(acceptedIDs, attackerID)
	}
	return acceptedIDs, rejected
}

// damageBuildingLocked chips a building or structure and demolishes it at
// zero HP, unlinking listening posts and clearing orders that reference it.
func (world *World) damageBuildingLocked(buildingID int, amount float64, killerOwner int) {
	destroyed := false
	if building, found := world.Buildings[buildingID]; found {
		building.HP -= amount
		destroyed = building.HP <= 0
	} else if structure, found := world.Structures[buildingID]; found {
		structure.HP -= amount
		destroyed = structure.HP <= 0
	} else {
		return
	}
	if destroyed {
		if killer := world.playerOf(killerOwner); killer != nil {
			killer.Kills++
		}
		world.destroyBuildingLocked(buildingID)
	}
}

// destroyBuildingLocked removes a razed building or structure and releases
// everything tied to it: listening-post links, worker orders and squads
// ordered to attack it.
func (world *World) destroyBuildingLocked(buildingID int) {
	if _, found := world.Buildings[buildingID]; found {
		delete(world.Buildings, buildingID)
	} else if _, found := world.Structures[buildingID]; found {
		delete(world.Structures, buildingID)
	} else {
		return
	}
	for _, point := range world.Points {
		if point.ListeningPostEntityID != nil && *point.ListeningPostEntityID == buildingID {
			point.ListeningPostEntityID = nil
			point.HasListeningPost = false
			point.LPTier = 0
		}
	}
	for _, builder := range world.Builders {
		if builder.BuildTarget != nil && *builder.BuildTarget == buildingID {
			builder.BuildTarget = nil
		}
	}
	for _, squad := range world.Squads {
		if squad.ForcedBuilding != nil && *squad.ForcedBuilding == buildingID {
			squad.ForcedBuilding = nil
		}
	}
}

// UnitCommand handles schema-2 move/attackmove/melee with optional enemy.
func (world *World) UnitCommand(playerID int, entityIDs []int, command string, target protocol.Vec3, enemySquad *int) (moved, rejected []int) {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	for _, entityID := range entityIDs {
		squad, found := world.Squads[entityID]
		if !found || squad.Owner != playerID {
			if builder, isValid := world.Builders[entityID]; isValid && builder.Owner == playerID {
				builderDestination := target
				builder.MoveTarget = &builderDestination
				clearBuilderBuildOrder(builder)
				moved = append(moved, entityID)
				continue
			}
			if vehicle, currentIsValid := world.Vehicles[entityID]; currentIsValid && vehicle.Owner == playerID {
				vehicleDestination := target
				vehicle.MoveOrder = &vehicleDestination
				moved = append(moved, entityID)
				continue
			}
			rejected = append(rejected, entityID)
			continue
		}
		squad.AttackMove = command != "move"
		squad.AutoChase = command != "move"
		squad.autoTarget = false
		squad.CaptureOrderSP = nil
		switch command {
		case "melee":
			squad.AttackMode = "melee"
			squad.Stopped = false
		case "attackmove":
			// Attack Move preserves the selected Ranged/Assault stance.
			squad.Stopped = false
		default:
			squad.Stopped = false
		}
		if enemySquad != nil {
			if enemySquadCandidate, enemyFound := world.Squads[*enemySquad]; enemyFound && enemySquadCandidate.Team != squad.Team {
				targetSquadID := *enemySquad
				squad.ForcedTarget = &targetSquadID
				squad.MoveOrder = nil
				moved = append(moved, entityID)
				continue
			}
		}
		squadDestination := target
		squad.MoveOrder = &squadDestination
		squad.ForcedTarget = nil
		moved = append(moved, entityID)
	}
	return moved, rejected
}

// SetStance sets stand/ranged stance.
func (world *World) SetStance(playerID int, entityIDs []int, stance string) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	isValid := false
	for _, entityID := range entityIDs {
		if squad, found := world.Squads[entityID]; found && squad.Owner == playerID {
			squad.Stance = stance
			if stance == "stand" {
				squad.Stopped = true
				squad.MoveOrder = nil
			} else {
				squad.AutoChase = true
			}
			isValid = true
		}
		if builder, found := world.Builders[entityID]; found && builder.Owner == playerID {
			builder.Stance = stance
			isValid = true
		}
		if vehicle, found := world.Vehicles[entityID]; found && vehicle.Owner == playerID {
			_ = vehicle
			isValid = true
		}
	}
	return isValid
}

// SetCombatStance sets melee/ranged attack mode (legacy name).
func (world *World) SetCombatStance(playerID int, entityIDs []int, mode string) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	isValid := false
	for _, entityID := range entityIDs {
		if squad, found := world.Squads[entityID]; found && squad.Owner == playerID {
			if mode == "melee" {
				squad.AttackMode = "melee"
			} else {
				squad.AttackMode = "ranged"
			}
			if squad.ForcedTarget != nil {
				squad.visualCooldown = 0
				for index := range squad.Soldiers {
					squad.Soldiers[index].MeleeTimer = 0
					squad.Soldiers[index].ShootTimer = 0
				}
			}
			isValid = true
		}
		if builder, found := world.Builders[entityID]; found && builder.Owner == playerID {
			builder.MeleeOrder = mode == "melee"
			isValid = true
		}
	}
	return isValid
}

// SetAutoChase toggles auto chase.
func (world *World) SetAutoChase(playerID int, entityIDs []int, enabled bool) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	isValid := false
	for _, entityID := range entityIDs {
		if squad, found := world.Squads[entityID]; found && squad.Owner == playerID {
			squad.AutoChase = enabled
			isValid = true
		}
	}
	return isValid
}

// SetRally sets a building rally point.
func (world *World) SetRally(playerID int, buildingID int, target protocol.Vec3) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	if building, isValid := world.Buildings[buildingID]; isValid && building.Owner == playerID {
		targetPosition := target
		building.Rally = &targetPosition
		return true
	}
	if vehicle, currentIsValid := world.Vehicles[buildingID]; currentIsValid && vehicle.Owner == playerID {
		currentTargetPosition := target
		vehicle.Rally = &currentTargetPosition
		return true
	}
	return false
}

// SetProjectileType changes a squad's projectile label.
func (world *World) SetProjectileType(playerID, squadID int, projectile string) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	squad, found := world.Squads[squadID]
	if !found || squad.Owner != playerID {
		return false
	}
	if projectile == "" {
		projectile = "Bullet"
	}
	squad.ProjectileType = projectile
	return true
}

// SetAutoReinforce toggles auto reinforce.
func (world *World) SetAutoReinforce(playerID, squadID int, enabled bool) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	squad, found := world.Squads[squadID]
	if !found || squad.Owner != playerID {
		return false
	}
	squad.AutoReinforce = enabled
	return true
}

// QueueReinforce adds one soldier slot (up to 13) as a simple model.
func (world *World) QueueReinforce(playerID, squadID int) bool {
	return world.QueueReinforceAt(playerID, squadID, 0)
}

// GrenadeUpgrade queues a grenade upgrade with cost/time.
func (world *World) GrenadeUpgrade(playerID, squadID int) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	squad, isValid := world.Squads[squadID]
	if !isValid || squad.Owner != playerID || squad.HasGrenade {
		return false
	}
	player := world.playerOf(playerID)
	if player == nil || player.Requisition < costOf("grenadeUpgrade") {
		return false
	}
	player.Requisition -= costOf("grenadeUpgrade")
	squad.GrenadeQueue = append(squad.GrenadeQueue, UpgradeItem{Cost: costOf("grenadeUpgrade"), Time: 5})
	return true
}

// PlasmaUpgrade queues a plasma upgrade with cost/time.
func (world *World) PlasmaUpgrade(playerID, squadID int) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	squad, isValid := world.Squads[squadID]
	if !isValid || squad.Owner != playerID || squad.HasPlasma {
		return false
	}
	player := world.playerOf(playerID)
	if player == nil || player.Requisition < costOf("plasmaUpgrade") {
		return false
	}
	player.Requisition -= costOf("plasmaUpgrade")
	squad.PlasmaQueue = append(squad.PlasmaQueue, UpgradeItem{Cost: costOf("plasmaUpgrade"), Time: 5})
	return true
}

// CancelUpgrade cancels a queued reinforce/upgrade and refunds.
func (world *World) CancelUpgrade(playerID, squadID int) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	squad, isValid := world.Squads[squadID]
	if !isValid || squad.Owner != playerID {
		return false
	}
	player := world.playerOf(playerID)
	refund := 0.0
	if len(squad.ReinforceQueue) > 0 {
		refund += squad.ReinforceQueue[len(squad.ReinforceQueue)-1].Cost
		squad.ReinforceQueue = squad.ReinforceQueue[:len(squad.ReinforceQueue)-1]
		squad.ReinforceProg = 0
	} else if len(squad.GrenadeQueue) > 0 {
		refund += squad.GrenadeQueue[len(squad.GrenadeQueue)-1].Cost
		squad.GrenadeQueue = squad.GrenadeQueue[:len(squad.GrenadeQueue)-1]
		squad.GrenadeProg = 0
	} else if len(squad.PlasmaQueue) > 0 {
		refund += squad.PlasmaQueue[len(squad.PlasmaQueue)-1].Cost
		squad.PlasmaQueue = squad.PlasmaQueue[:len(squad.PlasmaQueue)-1]
		squad.PlasmaProg = 0
	} else {
		return false
	}
	if player != nil {
		player.Requisition = math.Min(MaxRequisition, player.Requisition+refund)
	}
	return true
}

// SpendRequisition deducts resources (explicit spend action).
func (world *World) SpendRequisition(playerID int, amount float64) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	player := world.playerOf(playerID)
	if player == nil || player.Requisition < amount {
		return false
	}
	player.Requisition -= amount
	return true
}

// BuilderStance sets builder stance.
func (world *World) BuilderStance(playerID, builderID int, stance string) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	builder, isValid := world.Builders[builderID]
	if !isValid || builder.Owner != playerID {
		return false
	}
	builder.Stance = stance
	return true
}

// BuilderCommand handles builder move/repair orders.
func (world *World) BuilderCommand(playerID, builderID int, target protocol.Vec3, repairID *int) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	builder, isValid := world.Builders[builderID]
	if !isValid || builder.Owner != playerID {
		return false
	}
	targetPosition := target
	builder.MoveTarget = &targetPosition
	clearBuilderBuildOrder(builder)
	if repairID != nil {
		builder.RepairTarget = repairID
	}
	return true
}

// ResumeBuild reassigns a builder to finish a constructing entity.
func (world *World) ResumeBuild(playerID, builderID, targetID int) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	builder, buildingFound := world.Builders[builderID]
	if !buildingFound || builder.Owner != playerID {
		return false
	}
	if building, currentIsValid := world.Buildings[targetID]; currentIsValid {
		if (building.RequiresWorker || hasAnimatedConstruction(building.Faction, building.Type)) && (!building.Constructing || building.Owner != playerID) {
			return false
		}
		builder.BuildTarget = &targetID
		builder.BuildType = building.Type
		builder.BuildPos = &building.Pos
		builder.IsBuilding = true
		builder.BuildProg = 0
		if building.RequiresWorker || hasAnimatedConstruction(building.Faction, building.Type) {
			assignConstructionBuilder(builder, building.Pos, building.Faction, building.Type)
			building.PendingWorker = true
		}
		return true
	}
	if structure, structureFound := world.Structures[targetID]; structureFound {
		if (structure.RequiresWorker || hasAnimatedConstruction(structure.Faction, structure.Type)) && (!structure.Constructing || structure.Owner != playerID) {
			return false
		}
		builder.BuildTarget = &targetID
		builder.BuildType = structure.Type
		builder.BuildPos = &structure.Pos
		builder.IsBuilding = true
		builder.BuildProg = 0
		if structure.RequiresWorker || hasAnimatedConstruction(structure.Faction, structure.Type) {
			assignConstructionBuilder(builder, structure.Pos, structure.Faction, structure.Type)
			structure.PendingWorker = true
		}
		return true
	}
	return false
}

// BuildAddon records an addon and bumps tier.
func (world *World) BuildAddon(playerID, buildingID int, addon string) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	building, isValid := world.Buildings[buildingID]
	if !isValid || building.Owner != playerID || building.Constructing {
		return false
	}
	player := world.playerOf(playerID)
	cost := 50.0
	if player == nil || player.Requisition < cost {
		return false
	}
	for _, existingAddon := range building.Addons {
		if existingAddon == addon {
			return false
		}
	}
	player.Requisition -= cost
	building.Addons = append(building.Addons, addon)
	if building.Tier < 3 {
		building.Tier++
	}
	return true
}

// BoardTransport attaches squads to an APC (simplified: teleport together).
func (world *World) BoardTransport(playerID, apcID int, entityIDs []int) bool {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	vehicle, isValid := world.Vehicles[apcID]
	if !isValid || vehicle.Owner != playerID || vehicle.Kind != "apc" {
		return false
	}
	for _, entityID := range entityIDs {
		if squad, currentIsValid := world.Squads[entityID]; currentIsValid && squad.Owner == playerID {
			squad.Pos = vehicle.Pos
			squad.MoveOrder = nil
			squad.Stopped = true
		}
	}
	return true
}

// FlySquads orders squads to a distant target (simplified as fast move).
func (world *World) FlySquads(playerID int, entityIDs []int, target protocol.Vec3) (moved, rejected []int) {
	return world.MoveUnits(playerID, entityIDs, target, nil)
}

// --- Views ---

func soldierSlots(squad *Squad) []any {
	result := make([]any, len(squad.Soldiers))
	for index, soldier := range squad.Soldiers {
		if soldier.Alive {
			result[index] = map[string]any{"hp": soldier.HP, "max_hp": 100}
		} else {
			result[index] = nil
		}
	}
	return result
}

func (squad *Squad) toData() protocol.SquadData {
	var orderCopy *protocol.Vec3
	if squad.MoveOrder != nil {
		copied := *squad.MoveOrder
		orderCopy = &copied
	}
	return protocol.SquadData{
		Team: squad.Team, Position: squad.Pos, Facing: squad.Facing,
		Speed: squad.Speed, AttackRange: squad.AttackRange, DetectionRange: squad.DetectRange,
		GrenadeCd: 0, GrenadeReloadTime: 30, IdleTime: squad.IdleTime,
		ReinforceQueue: []any{}, ReinforceProgress: 0,
		GrenadeQueue: []any{}, GrenadeProgress: 0,
		MoveOrder: orderCopy, Stopped: squad.Stopped, AutoChase: squad.AutoChase,
		ProjectileType: squad.ProjectileType, Soldiers: soldierSlots(squad),
		ForcedTarget: squad.ForcedTarget, ForcedSoldierID: nil,
		AutoReinforce: squad.AutoReinforce, Owner: squad.Owner,
	}
}

// Snapshot renders the full state for _onSnapshot (legacy schema 1).
func (world *World) Snapshot() protocol.Snapshot {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	pairs := make([][]any, 0, len(world.Squads))
	for squadID, squad := range world.Squads {
		pairs = append(pairs, []any{squadID, squadToMap(squad)})
	}
	return protocol.Snapshot{Tick: world.Tick, Squads: pairs}
}

// Delta renders positions/facings for squads that moved.
func (world *World) Delta(full bool) protocol.Delta {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	delta := protocol.Delta{Tick: world.Tick, SquadPos: [][]float64{}, SquadFacing: [][]float64{}}
	for squadID, squad := range world.Squads {
		if full || squad.moved {
			delta.SquadPos = append(delta.SquadPos, []float64{float64(squadID), squad.Pos.X, squad.Pos.Z})
		}
		if full || squad.facingChanged {
			delta.SquadFacing = append(delta.SquadFacing, []float64{float64(squadID), squad.Facing.X, squad.Facing.Z})
		}
	}
	return delta
}

// squadToMap converts to the map shape the client parses (kept as a map so
// encoding/json emits the snake_case keys from SquadData tags).
func squadToMap(squad *Squad) map[string]any {
	data := squad.toData()
	return map[string]any{
		"faction": squad.Faction, "team": data.Team, "position": data.Position, "facing": data.Facing,
		"speed": data.Speed, "attack_range": data.AttackRange, "detection_range": data.DetectionRange,
		"grenade_cd": data.GrenadeCd, "grenade_reload_time": data.GrenadeReloadTime,
		"idle_time": data.IdleTime, "reinforce_queue": data.ReinforceQueue,
		"reinforce_progress": data.ReinforceProgress, "grenade_queue": data.GrenadeQueue,
		"grenade_progress": data.GrenadeProgress, "move_order": data.MoveOrder,
		"stopped": data.Stopped, "auto_chase": data.AutoChase,
		"projectile_type": data.ProjectileType, "soldiers": data.Soldiers,
		"forced_target": data.ForcedTarget, "forced_soldier_id": data.ForcedSoldierID,
		"auto_reinforce": data.AutoReinforce, "owner": data.Owner,
	}
}

// --- Snapshot v2 (skirmish parity: entities/players/winner) ---

func vecBox(vector protocol.Vec3) map[string]any {
	return map[string]any{"$v": []float64{vector.X, vector.Y, vector.Z}}
}

func yawOf(facing protocol.Vec3) float64 {
	return math.Atan2(facing.X, facing.Z)
}

func lowerTeam(team string) string {
	if team == "Red" {
		return "red"
	}
	return "blue"
}

func nonNullList(value any) any {
	if value == nil {
		return []any{}
	}
	reflectedValue := reflect.ValueOf(value)
	if (reflectedValue.Kind() == reflect.Slice || reflectedValue.Kind() == reflect.Array) && reflectedValue.IsNil() {
		return []any{}
	}
	return value
}

func (world *World) snapshotV2Locked() map[string]any {
	world.refreshNavigationLocked()
	entities := []any{}
	emit := func(entityID int, kind string, component map[string]any, position protocol.Vec3, facing protocol.Vec3, owner int) {
		yaw := yawOf(facing)
		entities = append(entities, map[string]any{
			"id": entityID,
			"components": map[string]any{
				kind:           component,
				"networkOwner": map[string]any{"id": owner},
			},
			"transform": map[string]any{
				"p":       []float64{position.X, position.Y, position.Z},
				"r":       []float64{0, yaw, 0},
				"visible": true,
				"s":       []float64{1, 1, 1},
			},
		})
	}
	for squadID, squad := range world.Squads {
		soldierIDs := make([]any, 0, len(squad.Soldiers))
		for index := range squad.Soldiers {
			soldierEntityID := squad.Soldiers[index].EntityID
			if soldierEntityID == 0 {
				soldierEntityID = squad.ID*1000 + index + 1
				squad.Soldiers[index].EntityID = soldierEntityID
			}
			if squad.Soldiers[index].Alive {
				soldierIDs = append(soldierIDs, soldierEntityID)
			} else {
				soldierIDs = append(soldierIDs, nil)
			}
		}
		var moveOrder any
		if squad.MoveOrder != nil {
			moveOrder = vecBox(*squad.MoveOrder)
		}
		squadComponent := map[string]any{
			"team": lowerTeam(squad.Team), "faction": squad.Faction, "unitType": squad.UnitType,
			"position": vecBox(squad.Pos), "facing": vecBox(squad.Facing),
			"speed": squad.Speed, "attackRange": squad.AttackRange, "detectionRange": squad.DetectRange,
			"maxSquadSize": 13, "reinforceCost": 20.0, "reinforceTime": 7.0,
			"grenadeCd": squad.GrenadeCd, "grenadeReloadTime": 3.0, "idleTime": squad.IdleTime,
			"reinforceQueue": nonNullList(squad.ReinforceQueue), "reinforceProgress": squad.ReinforceProg,
			"grenadeQueue": nonNullList(squad.GrenadeQueue), "grenadeProgress": squad.GrenadeProg,
			"plasmaQueue": nonNullList(squad.PlasmaQueue), "plasmaProgress": squad.PlasmaProg,
			"moveOrder": moveOrder, "stopped": squad.Stopped,
			"captureOrderSpId": squad.CaptureOrderSP, "capturingSpId": squad.CapturingSP,
			"meleeEngaged": squad.meleeOpponent != nil, "autoChase": squad.AutoChase, "attackMove": squad.AttackMove, "projectileType": squad.ProjectileType,
			"soldiers": soldierIDs, "forcedTarget": squad.ForcedTarget,
			"autoReinforce": squad.AutoReinforce, "attackMode": squad.AttackMode, "stance": squad.Stance,
		}
		if world.dota != nil {
			squadComponent["maxSquadSize"] = 1
			squadComponent["dotaHero"] = ValidDotaHero(squad.UnitType)
		}
		emit(squadID, "squad", squadComponent, squad.Pos, squad.Facing, squad.Owner)
		// soldier entities
		for soldierIndex := range squad.Soldiers {
			soldier := &squad.Soldiers[soldierIndex]
			if !soldier.Alive {
				continue
			}
			if soldier.EntityID == 0 {
				soldier.EntityID = squad.ID*1000 + soldierIndex + 1
			}
			formationOffsetX := float64((soldierIndex%3)-1) * 2.2
			formationOffsetZ := float64((soldierIndex/3)-1) * 2.2
			soldierPosition := protocol.Vec3{X: squad.Pos.X + formationOffsetX, Z: squad.Pos.Z + formationOffsetZ}
			if world.dota != nil {
				soldierPosition = squad.Pos
			}
			soldierPosition = projectGroundGoal(soldierPosition, squad.Pos, world.groundBoundsLocked(.5))
			maximumHealthPoints := soldier.MaxHP
			if maximumHealthPoints == 0 {
				maximumHealthPoints = 100
			}
			soldierComponent := map[string]any{
				"squadId": squad.ID, "formIdx": soldier.FormIdx, "unitType": soldier.UnitType,
				"hp": soldier.HP, "maxHp": maximumHealthPoints, "attackCooldown": squad.visualCooldown,
				"meleeTimer": soldier.MeleeTimer, "shootTimer": soldier.ShootTimer, "hitReactionTimer": soldier.HitReactionTimer, "hitReactionAnim": soldier.HitReactionAnim,
				"meleeEngaged": squad.meleeOpponent != nil, "meleeAttackSerial": soldier.MeleeSerial, "shotSerial": soldier.ShotSerial, "_meleeReactionIndex": soldier.ReactionSerial, "targetId": soldier.TargetID,
				"animation": soldierAnimation(squad, soldier, soldierIndex), "moving": squad.moved,
				"animationSerial": soldier.MeleeSerial + soldier.ShotSerial + soldier.ReactionSerial,
			}
			emit(soldier.EntityID, "soldier", soldierComponent, soldierPosition, squad.Facing, squad.Owner)
		}
	}
	for builderID, builder := range world.Builders {
		builderComponent := map[string]any{
			"team": lowerTeam(builder.Team), "faction": builder.Faction, "hp": builder.HP, "maxHp": builder.MaxHP,
			"speed": builder.Speed, "stance": builder.Stance, "attackMove": builder.AttackMove,
			"meleeOrder": builder.MeleeOrder, "isBuilding": builder.IsBuilding, "buildProgress": builder.BuildProg,
			"animation": builderAnimation(builder), "moving": builder.moved, "repairTarget": builder.RepairTarget,
		}
		facing := protocol.Vec3{Z: 1}
		if builder.IsBuilding && builder.BuildPos != nil {
			facing = protocol.Vec3{X: builder.BuildPos.X - builder.Pos.X, Z: builder.BuildPos.Z - builder.Pos.Z}
		} else if builder.MoveTarget != nil {
			facing = protocol.Vec3{X: builder.MoveTarget.X - builder.Pos.X, Z: builder.MoveTarget.Z - builder.Pos.Z}
		}
		emit(builderID, "builder", builderComponent, builder.Pos, facing, builder.Owner)
	}
	for buildingID, building := range world.Buildings {
		var rally any
		if building.Rally != nil {
			rally = vecBox(*building.Rally)
		}
		buildingComponent := map[string]any{
			"type": building.Type, "heroBase": building.HeroBase, "position": vecBox(building.Pos), "team": lowerTeam(building.Team), "faction": building.Faction,
			"hp": building.HP, "maxHp": building.MaxHP, "queue": nonNullList(building.Queue),
			"researchQueue": nonNullList(building.Research), "addons": nonNullList(building.Addons), "tier": building.Tier,
			"rallyPoint": rally, "constructing": building.Constructing,
			"constructProgress": building.Progress, "constructTime": building.ConstructTime,
			"pendingWorker": building.PendingWorker, "deliveryProgress": building.DeliveryProgress, "deliveryTime": building.DeliveryTime,
			"requiresWorker":   building.RequiresWorker,
			"strategicPointId": building.StrategicPointID,
		}
		facing := protocol.Vec3{Z: 1}
		if building.Faction == "ig" && (building.Type == "hq" || building.Type == "hq-anim") {
			facing = protocol.Vec3{X: 1}
		}
		emit(buildingID, "building", buildingComponent, building.Pos, facing, building.Owner)
	}
	for structureID, structure := range world.Structures {
		structureComponent := map[string]any{
			"type": structure.Type, "team": lowerTeam(structure.Team), "faction": structure.Faction,
			"position": vecBox(structure.Pos), "hp": structure.HP, "maxHp": structure.MaxHP,
			"attackRange": structure.AttackRange, "attackRate": structure.AttackRate,
			"constructing": structure.Constructing, "constructProgress": structure.Progress,
			"constructTime": structure.ConstructTime, "researchQueue": nonNullList(structure.Research),
			"pendingWorker": structure.PendingWorker, "deliveryProgress": structure.DeliveryProgress, "deliveryTime": structure.DeliveryTime,
			"requiresWorker": structure.RequiresWorker,
		}
		emit(structureID, "structure", structureComponent, structure.Pos, protocol.Vec3{X: 0, Z: 1}, structure.Owner)
	}
	for vehicleID, vehicle := range world.Vehicles {
		kind := vehicle.Kind
		if kind == "" {
			kind = "vehicle"
		}
		var vehicleMoveOrder any
		if vehicle.MoveOrder != nil {
			vehicleMoveOrder = vecBox(*vehicle.MoveOrder)
		}
		var rally any
		if vehicle.Rally != nil {
			rally = vecBox(*vehicle.Rally)
		}
		vehicleComponent := map[string]any{
			"name": vehicle.Name, "model": vehicle.Model, "icon": vehicle.Icon, "projectileType": vehicle.ProjectileType,
			"team": lowerTeam(vehicle.Team), "faction": vehicle.Faction, "position": vecBox(vehicle.Pos),
			"hp": vehicle.HP, "maxHp": vehicle.MaxHP, "moveOrder": vehicleMoveOrder,
			"constructing": vehicle.Constructing, "constructProgress": vehicle.Progress,
			"constructTime": vehicle.ConstructTime, "queue": nonNullList(vehicle.Queue), "rallyPoint": rally,
		}
		emit(vehicleID, kind, vehicleComponent, vehicle.Pos, protocol.Vec3{X: 0, Z: 1}, vehicle.Owner)
	}
	for pointID, point := range world.Points {
		var owner any
		if point.Owner == "Blue" {
			owner = "blue"
		} else if point.Owner == "Red" {
			owner = "red"
		} else {
			owner = nil
		}
		pointComponent := map[string]any{
			"kind": point.Kind, "position": vecBox(point.Pos), "owner": owner,
			"faction": point.Faction, "captureProgress": point.CaptureProgress,
			"captureRadius": CaptureRadius, "hasListeningPost": point.HasListeningPost,
			"lpTier": point.LPTier, "researchQueue": nonNullList(point.Research), "raiseFrom": point.RaiseFrom,
			"listeningPostEntityId": point.ListeningPostEntityID,
		}
		emit(pointID, "strategicPoint", pointComponent, point.Pos, protocol.Vec3{X: 0, Z: 1}, 0)
	}
	players := []any{}
	for _, player := range world.Players {
		if player.ID < 0 {
			continue
		}
		completedResearch := []string{}
		for researchKey, done := range player.Research {
			if done {
				completedResearch = append(completedResearch, researchKey)
			}
		}
		teamLower := "blue"
		if player.Team == "Red" {
			teamLower = "red"
		}
		players = append(players, map[string]any{
			"id": player.ID, "team": teamLower, "faction": player.Faction,
			"requisition": player.Requisition, "energy": player.Energy,
			"requisitionRate": world.requisitionRateLocked(player), "powerRate": world.powerRateLocked(player),
			"kills": player.Kills, "research": completedResearch,
		})
	}
	result := map[string]any{
		"schema": 2, "tick": world.Tick, "entities": entities, "players": players, "heightMode": "terrain",
	}
	if world.Winner != "" {
		result["winner"] = world.Winner
	}
	return result
}

// SnapshotV2 renders the skirmish-parity full state.
func (world *World) SnapshotV2() protocol.SnapshotV2 {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	snapshotData := world.snapshotV2Locked()
	entities, _ := snapshotData["entities"].([]any)
	players, _ := snapshotData["players"].([]any)
	var winner *string
	if text, isValid := snapshotData["winner"].(string); isValid {
		winner = &text
	}
	return protocol.SnapshotV2{Schema: 2, Tick: world.Tick, Entities: entities, Players: players, Winner: winner}
}

// SnapshotFrameV2 returns the wire envelope for transports.
func (world *World) SnapshotFrameV2() map[string]any {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	return map[string]any{"Snapshot": world.snapshotV2Locked()}
}

func aliveCount(squad *Squad) int {
	entityCount := 0
	for _, soldier := range squad.Soldiers {
		if soldier.Alive {
			entityCount++
		}
	}
	return entityCount
}

// ResetMatchClock clears any result from a previous match in a reused room.
func (world *World) ResetMatchClock() {
	world.mutex.Lock()
	defer world.mutex.Unlock()
	world.Tick = 0
	world.Winner = ""
}
