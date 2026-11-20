package sim

import (
	"container/heap"
	_ "embed"
	"encoding/json"
	"math"

	"grimcraft-go/internal/protocol"
)

//go:embed data/dota-nav.json
var dotaNavData []byte

type dotaNavigation struct {
	Grid struct {
		SizeX, SizeZ                int
		OriginX, OriginZ, CellWorld float64
	}
	Rows [][]uint8
}

var dotaNav = func() dotaNavigation {
	var navigation dotaNavigation
	if err := json.Unmarshal(dotaNavData, &navigation); err != nil {
		panic(err)
	}
	return navigation
}()

func (nav dotaNavigation) cell(pos protocol.Vec3) (int, int) {
	return int(math.Floor((pos.X - nav.Grid.OriginX) / nav.Grid.CellWorld)),
		int(math.Floor((pos.Z - nav.Grid.OriginZ) / nav.Grid.CellWorld))
}

func (nav dotaNavigation) point(x, z int) protocol.Vec3 {
	return protocol.Vec3{X: nav.Grid.OriginX + (float64(x)+.5)*nav.Grid.CellWorld,
		Z: nav.Grid.OriginZ + (float64(z)+.5)*nav.Grid.CellWorld}
}

func (nav dotaNavigation) open(x, z int) bool {
	return x >= 0 && x < nav.Grid.SizeX && z >= 0 && z < nav.Grid.SizeZ && nav.Rows[z][x] == 2
}

func (nav dotaNavigation) snap(pos protocol.Vec3) protocol.Vec3 {
	x, z := nav.cell(pos)
	if nav.open(x, z) {
		return pos
	}
	x = max(0, min(x, nav.Grid.SizeX-1))
	z = max(0, min(z, nav.Grid.SizeZ-1))
	best := math.Inf(1)
	result := pos
	for row := max(0, z-16); row <= min(nav.Grid.SizeZ-1, z+16); row++ {
		for column := max(0, x-16); column <= min(nav.Grid.SizeX-1, x+16); column++ {
			if !nav.open(column, row) {
				continue
			}
			point := nav.point(column, row)
			if distance := dist(pos, point); distance < best {
				best, result = distance, point
			}
		}
	}
	return result
}

func (nav dotaNavigation) segmentOpen(from, to protocol.Vec3) bool {
	samples := max(1, int(math.Ceil(dist(from, to)/(nav.Grid.CellWorld*.35))))
	for index := 1; index <= samples; index++ {
		t := float64(index) / float64(samples)
		point := protocol.Vec3{X: from.X + (to.X-from.X)*t, Z: from.Z + (to.Z-from.Z)*t}
		x, z := nav.cell(point)
		if !nav.open(x, z) {
			return false
		}
	}
	return true
}

type dotaNode struct{ id, cost, priority int }
type dotaQueue []dotaNode

func (q dotaQueue) Len() int           { return len(q) }
func (q dotaQueue) Less(i, j int) bool { return q[i].priority < q[j].priority }
func (q dotaQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *dotaQueue) Push(value any)    { *q = append(*q, value.(dotaNode)) }
func (q *dotaQueue) Pop() any {
	item := (*q)[len(*q)-1]
	*q = (*q)[:len(*q)-1]
	return item
}

func (nav dotaNavigation) route(from, goal protocol.Vec3) []protocol.Vec3 {
	if nav.segmentOpen(from, goal) {
		return []protocol.Vec3{goal}
	}
	xf, zf := nav.cell(nav.snap(from))
	xg, zg := nav.cell(goal)
	if !nav.open(xf, zf) || !nav.open(xg, zg) {
		return []protocol.Vec3{from}
	}
	width := nav.Grid.SizeX
	start, end := zf*width+xf, zg*width+xg
	if start == end {
		return []protocol.Vec3{goal}
	}
	frontier := &dotaQueue{{id: start}}
	costs := map[int]int{start: 0}
	previous := map[int]int{}
	abs := func(n int) int { return max(n, -n) }
	for expanded := 0; frontier.Len() > 0 && expanded < 24000; expanded++ {
		node := heap.Pop(frontier).(dotaNode)
		if node.cost != costs[node.id] {
			continue
		}
		if node.id == end {
			reverse := []protocol.Vec3{goal, nav.point(xg, zg)}
			for id := previous[end]; id != start; id = previous[id] {
				reverse = append(reverse, nav.point(id%width, id/width))
			}
			reverse = append(reverse, nav.point(xf, zf))
			result := make([]protocol.Vec3, len(reverse))
			for i, point := range reverse {
				result[len(reverse)-i-1] = point
			}
			return result
		}
		for _, step := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			x, z := node.id%width+step[0], node.id/width+step[1]
			if !nav.open(x, z) {
				continue
			}
			id, cost := z*width+x, node.cost+1
			if best, found := costs[id]; found && best <= cost {
				continue
			}
			costs[id], previous[id] = cost, node.id
			heap.Push(frontier, dotaNode{id: id, cost: cost, priority: cost + abs(x-xg) + abs(z-zg)})
		}
	}
	return []protocol.Vec3{from}
}

func (world *World) dotaWaypointLocked(entityID int, from, goal protocol.Vec3) (protocol.Vec3, protocol.Vec3) {
	goal = dotaNav.snap(goal)
	cached := world.navigation.Paths[entityID]
	if cached == nil || dist(cached.Goal, goal) > dotaNav.Grid.CellWorld || len(cached.Points) == 0 {
		cached = &groundPath{Goal: goal, Points: dotaNav.route(from, goal)}
		world.navigation.Paths[entityID] = cached
	}
	// Skip visibility-free corners while keeping turns away from cliffs.
	for len(cached.Points) > 1 && dist(cached.Points[0], from) < 3 && dotaNav.segmentOpen(from, cached.Points[1]) {
		cached.Points = cached.Points[1:]
	}
	return cached.Points[0], cached.Goal
}
