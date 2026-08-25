//go:build darwin

package tray

// assignSlots splits the (sorted) config names into the inline menu slots
// and the overflow submenu. The active config is always inline: when it
// would land in overflow it takes the last slot, and the config it
// displaces moves to overflow (keeping sort order there).
func assignSlots(configs []string, active string, slots int) (inline, overflow []string) {
	if len(configs) <= slots {
		return append([]string(nil), configs...), nil
	}
	inline = append([]string(nil), configs[:slots]...)
	overflow = append([]string(nil), configs[slots:]...)
	for i, name := range overflow {
		if name == active {
			displaced := inline[slots-1]
			inline[slots-1] = name
			overflow = append(overflow[:i], overflow[i+1:]...)
			// keep overflow sorted: displaced comes from before any
			// remaining overflow entry, so it goes to the front
			overflow = append([]string{displaced}, overflow...)
			break
		}
	}
	return inline, overflow
}
