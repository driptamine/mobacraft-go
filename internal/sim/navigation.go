package sim

import (
	"grimcraft-go/internal/protocol"
	"math"
)

type groundPath struct {
	Revision uint64
	Goal     protocol.Vec3
	Radius   float64
	Points   []protocol.Vec3
}
type navigationState struct {
	Expanded map[float64][]footprintBounds
	Bounds   []footprintBounds
	Revision uint64
	Paths    map[int]*groundPath
}

func (world *World) refreshNavigationLocked() {
	bounds := []footprintBounds{}
	for _, id := range sortedEntityIDs(world.Buildings) {
		site := world.Buildings[id]
		if site.HP > 0 {
			faction := site.Faction
			if site.HeroBase {
				// The Dota base is the standard 10-unit square without a faction model.
				faction = ""
			}
			bounds = append(bounds, structureFootprint(faction, site.Type, site.Constructing || site.PendingWorker, site.Pos))
		}
	}
	for _, id := range sortedEntityIDs(world.Structures) {
		site := world.Structures[id]
		if site.HP > 0 {
			bounds = append(bounds, structureFootprint(site.Faction, site.Type, site.Constructing || site.PendingWorker, site.Pos))
		}
	}
	for _, id := range sortedEntityIDs(world.Vehicles) {
		site := world.Vehicles[id]
		if site.HP > 0 && site.Kind == "airfield" {
			bounds = append(bounds, structureFootprint(site.Faction, "airfield", site.Constructing, site.Pos))
		}
	}
	changed := len(bounds) != len(world.navigation.Bounds)
	if !changed {
		for index := range bounds {
			if bounds[index] != world.navigation.Bounds[index] {
				changed = true
				break
			}
		}
	}
	if changed {
		world.navigation.Bounds = bounds
		world.navigation.Revision++
		world.navigation.Paths = map[int]*groundPath{}
		world.navigation.Expanded = nil
	}
	if world.navigation.Paths == nil {
		world.navigation.Paths = map[int]*groundPath{}
	}
}

func (world *World) groundBoundsLocked(radius float64) []footprintBounds {
	if world.navigation.Expanded == nil {
		world.navigation.Expanded = map[float64][]footprintBounds{}
	}
	if bounds, found := world.navigation.Expanded[radius]; found {
		return bounds
	}
	bounds := expandGroundBounds(world.navigation.Bounds, radius)
	world.navigation.Expanded[radius] = bounds
	return bounds
}

func expandGroundBounds(footprints []footprintBounds, radius float64) []footprintBounds {
	bounds := make([]footprintBounds, 0, len(footprints))
	padding := radius + .2
	for _, contour := range footprints {
		bounds = append(bounds, footprintBounds{contour.MinX - padding, contour.MaxX + padding, contour.MinZ - padding, contour.MaxZ + padding})
	}
	return bounds
}
func pointInsideGround(point protocol.Vec3, bounds []footprintBounds) bool {
	for _, contour := range bounds {
		if insideFootprint(point, contour, 0) {
			return true
		}
	}
	return false
}
func groundLineClear(from, to protocol.Vec3, bounds []footprintBounds, escape bool) bool {
	for _, contour := range bounds {
		if escape && insideFootprint(from, contour, 0) {
			continue
		}
		if segmentEntersFootprint(from, to, contour) {
			return false
		}
	}
	return true
}
func projectGroundGoal(point, from protocol.Vec3, bounds []footprintBounds) protocol.Vec3 {
	if !pointInsideGround(point, bounds) {
		return point
	}
	candidates := []protocol.Vec3{}
	gap := .02
	for _, contour := range bounds {
		clampedX := math.Max(contour.MinX, math.Min(contour.MaxX, point.X))
		clampedZ := math.Max(contour.MinZ, math.Min(contour.MaxZ, point.Z))
		candidates = append(candidates, protocol.Vec3{X: clampedX, Y: point.Y, Z: contour.MinZ - gap}, protocol.Vec3{X: clampedX, Y: point.Y, Z: contour.MaxZ + gap}, protocol.Vec3{X: contour.MinX - gap, Y: point.Y, Z: clampedZ}, protocol.Vec3{X: contour.MaxX + gap, Y: point.Y, Z: clampedZ})
		for _, coordinateX := range []float64{contour.MinX - gap, contour.MaxX + gap} {
			for _, coordinateZ := range []float64{contour.MinZ - gap, contour.MaxZ + gap} {
				candidates = append(candidates, protocol.Vec3{X: coordinateX, Y: point.Y, Z: coordinateZ})
			}
		}
	}
	best, bestDistance := point, math.Inf(1)
	for _, candidate := range candidates {
		if pointInsideGround(candidate, bounds) {
			continue
		}
		distance := dist(point, candidate) + dist(from, candidate)*.001
		if distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best
}

// Visibility graph over expanded building corners. The path is cached per
// entity/order and invalidated when construction changes the actual contour.
func groundBuildingPath(from, goal protocol.Vec3, bounds []footprintBounds) []protocol.Vec3 {
	if pointInsideGround(from, bounds) {
		return []protocol.Vec3{projectGroundGoal(from, goal, bounds)}
	}
	if groundLineClear(from, goal, bounds, false) {
		return []protocol.Vec3{goal}
	}
	points := []protocol.Vec3{from, goal}
	for _, contour := range bounds {
		for _, coordinateX := range []float64{contour.MinX, contour.MaxX} {
			for _, coordinateZ := range []float64{contour.MinZ, contour.MaxZ} {
				candidate := protocol.Vec3{X: coordinateX, Z: coordinateZ}
				if !pointInsideGround(candidate, bounds) {
					points = append(points, candidate)
				}
			}
		}
	}
	distances, previous, visited := make([]float64, len(points)), make([]int, len(points)), make([]bool, len(points))
	for index := range points {
		distances[index] = math.Inf(1)
		previous[index] = -1
	}
	distances[0] = 0
	for range points {
		current := -1
		for index := range points {
			if !visited[index] && (current < 0 || distances[index] < distances[current]) {
				current = index
			}
		}
		if current < 0 || math.IsInf(distances[current], 1) || current == 1 {
			break
		}
		visited[current] = true
		for next := range points {
			if next == current || visited[next] || !groundLineClear(points[current], points[next], bounds, false) {
				continue
			}
			distance := distances[current] + dist(points[current], points[next])
			if distance < distances[next] {
				distances[next] = distance
				previous[next] = current
			}
		}
	}
	if previous[1] < 0 {
		return []protocol.Vec3{from}
	}
	reversed := []protocol.Vec3{}
	for index := 1; index > 0; index = previous[index] {
		reversed = append(reversed, points[index])
	}
	route := make([]protocol.Vec3, len(reversed))
	for index := range reversed {
		route[len(reversed)-1-index] = reversed[index]
	}
	return route
}

func (world *World) groundWaypointLocked(entityID int, from, requested protocol.Vec3, radius float64) (protocol.Vec3, protocol.Vec3) {
	if world.dota != nil {
		return world.dotaWaypointLocked(entityID, from, requested)
	}
	if len(world.navigation.Bounds) == 0 {
		return requested, requested
	}
	bounds := world.groundBoundsLocked(radius)
	destination := projectGroundGoal(requested, from, bounds)
	cached := world.navigation.Paths[entityID]
	if cached == nil || cached.Revision != world.navigation.Revision || cached.Radius != radius || dist(cached.Goal, destination) > .25 || len(cached.Points) == 0 {
		cached = &groundPath{Revision: world.navigation.Revision, Goal: destination, Radius: radius, Points: groundBuildingPath(from, destination, bounds)}
		world.navigation.Paths[entityID] = cached
	}
	for len(cached.Points) > 1 && dist(from, cached.Points[0]) < 1.5 && groundLineClear(from, cached.Points[1], bounds, false) {
		cached.Points = cached.Points[1:]
	}
	if len(cached.Points) == 1 && dist(from, cached.Points[0]) < .15 && dist(from, destination) > .3 {
		cached.Points = groundBuildingPath(from, destination, bounds)
	}
	return cached.Points[0], destination
}
func (world *World) groundStepLocked(from, target protocol.Vec3, step, radius float64) protocol.Vec3 {
	distance := dist(from, target)
	if distance < 1e-9 || step <= 0 {
		return from
	}
	fraction := math.Min(1, step/distance)
	next := protocol.Vec3{X: from.X + (target.X-from.X)*fraction, Y: from.Y, Z: from.Z + (target.Z-from.Z)*fraction}
	if world.dota != nil && !dotaNav.segmentOpen(from, next) {
		return from
	}
	if !groundLineClear(from, next, world.groundBoundsLocked(radius), true) {
		return from
	}
	return next
}
