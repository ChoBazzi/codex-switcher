// Package accountslot defines stable local positions, never account identities.
package accountslot

const Capacity = 5

func All() []string { return []string{"a", "b", "c", "d", "e"} }

func Valid(slot string) bool { return len(slot) == 1 && slot[0] >= 'a' && slot[0] <= 'e' }

func Index(slot string) int {
	if !Valid(slot) {
		return -1
	}
	return int(slot[0] - 'a')
}
