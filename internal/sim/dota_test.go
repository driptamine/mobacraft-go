package sim

import "testing"

func TestDotaMatchUsesSelectedHeroesAndSendsWavesDownAllLanes(t *testing.T) {
	world := NewWorld()
	world.StartDotaMatch([]DotaSeat{
		{Owner: 1, Team: "Blue", Hero: "juggernaut"},
		{Owner: 2, Team: "Red", Hero: "drow"},
	})
	if len(world.Buildings) != 2 || len(world.Builders) != 0 || len(world.Points) != 0 || len(world.Squads) != 32 {
		t.Fatal("Dota must begin with two bases, two heroes and thirty lane creeps")
	}
	for _, base := range world.Buildings {
		if !base.HeroBase || base.Team == "Blue" && (base.Pos.X >= 0 || base.Pos.Z >= 0) || base.Team == "Red" && (base.Pos.X <= 0 || base.Pos.Z <= 0) {
			t.Fatalf("incorrect Dota base: %+v", base)
		}
	}
	var hero *Squad
	var opponent *Squad
	for _, squad := range world.Squads {
		if unit, creep := dotaUnits.Creeps[squad.UnitType]; creep {
			if unit.Melee && (squad.AttackMode != "melee" || squad.ProjectileType != "") {
				t.Fatalf("melee creep has a ranged weapon: %s", squad.UnitType)
			}
			if !unit.Melee && squad.ProjectileType == "" {
				t.Fatalf("ranged creep has no projectile: %s", squad.UnitType)
			}
		}
		switch squad.Owner {
		case 1:
			hero = squad
		case 2:
			opponent = squad
		}
	}
	if hero == nil || hero.UnitType != "juggernaut" || opponent == nil || opponent.UnitType != "drow" {
		t.Fatal("server did not spawn the chosen heroes")
	}
	layout := layoutFor("Dota 2")
	if hero.Pos != layout.HeroStarts["blue"] || opponent.Pos != layout.HeroStarts["red"] || dist(hero.Pos, opponent.Pos) < 25 {
		t.Fatalf("expected two central starts on opposite sides of the river: %+v, %+v", hero.Pos, opponent.Pos)
	}
	start := hero.Pos
	destination := start
	destination.X += 10
	destination.Z += 8
	if moved, _ := world.MoveUnits(1, []int{hero.ID, opponent.ID}, destination, nil); len(moved) != 1 || moved[0] != hero.ID {
		t.Fatal("the player must only control their own hero")
	}
	for range 200 {
		world.Advance(.1)
	}
	if dist(hero.Pos, start) < 3 {
		t.Fatal("hero failed to move on the Dota terrain")
	}
	advancedLanes := map[[2]string]bool{}
	for _, walker := range world.dota.walkers {
		if walker.next >= 2 {
			advancedLanes[[2]string{walker.team, walker.lane}] = true
		}
	}
	if len(advancedLanes) != 6 {
		t.Fatalf("creeps did not progress from the spawn on all six lanes: %v", advancedLanes)
	}
	for range 110 {
		world.Advance(.1)
	}
	if len(world.dota.walkers) <= 30 {
		t.Fatal("the server did not spawn a second creep wave")
	}
}
