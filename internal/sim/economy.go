package sim

import "grimcraft-go/internal/botai"

const resourceRateSeconds = 10.0

// Energy economy: every race starts at 100 energy with zero income.
// Each completed generator adds EnergyPerGenerator per 10s, capped at
// MaxGeneratorsPerHQ per completed headquarters — a second HQ doubles the cap.
const (
	MaxGeneratorsPerHQ = 6
	EnergyPerGenerator = 10.0
)

var listeningPostRequisitionRates = [...]float64{6, 12, 18, 24}

func initialResources(faction string) (float64, float64) {
	if canonicalFaction(faction) == "necrons" {
		return 1500, 100
	}
	return 1000, 100
}

// Values returned here match the HUD's ten-second income period.
func (world *World) requisitionRateLocked(player *Player) float64 {
	baseRate := 20.0
	if canonicalFaction(player.Faction) == "necrons" {
		baseRate = 10
	} // Preserve the existing Necron income.
	rate := baseRate
	for _, point := range world.Points {
		if point.Owner != player.Team {
			continue
		}
		tier := point.LPTier
		if point.HasListeningPost && tier < 1 {
			tier = 1
		}
		tier = max(0, min(3, tier))
		rate += listeningPostRequisitionRates[tier]
	}
	return rate
}
func (world *World) powerRateLocked(player *Player) float64 {
	headquarters := 0
	for _, building := range world.Buildings {
		if building.Owner == player.ID && !building.Constructing && (building.Type == "hq" || building.Type == "hq-anim") {
			headquarters++
		}
	}
	generators := 0
	for _, structure := range world.Structures {
		if structure.Owner == player.ID && !structure.Constructing && !structure.PendingWorker && botai.IsGenerator(structure.Type) {
			generators++
		}
	}
	return float64(min(generators, MaxGeneratorsPerHQ*headquarters)) * EnergyPerGenerator
}
