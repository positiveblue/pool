package ban

// Info serves as a helper struct to store all ban-related information for a
// trader ban.
type Info struct {
	// Height is the height at which the ban begins to apply.
	Height uint32

	// Duration is the number of blocks the ban will last for once applied.
	Duration uint32
}

// Expiration returns the height at which the ban expires.
func (i *Info) Expiration() uint32 {
	return i.Height + i.Duration
}

// ExceedsBanExpiration determines whether the given height exceeds the ban
// expiration height.
func (i *Info) ExceedsBanExpiration(currentHeight uint32) bool {
	return currentHeight >= i.Expiration()
}
