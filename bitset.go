package protocol

import "math/big"

// Bitset is a representation of std::bitset<size> being sent over the network, allowing for more than 64 bits
// to be stored in a single integer. A Bitset has a fixed size, which is set at creation time.
type Bitset struct {
	size int
	int  *big.Int
}

// NewBitset creates a new Bitset with a specific size. The size is the amount of bits that the Bitset can
// store. Attempting to set a bit at an index higher than the size will panic.
func NewBitset(size int) Bitset {
	return Bitset{size: size, int: new(big.Int)}
}

// Set sets a bit at a specific index in the Bitset. Indices outside the Bitset are
// ignored.
func (b Bitset) Set(i int) {
	if i < 0 || i >= b.size {
		return
	}
	b.int.SetBit(b.int, i, 1)
}

// Unset unsets a bit at a specific index in the Bitset. Indices outside the Bitset
// are ignored.
func (b Bitset) Unset(i int) {
	if i < 0 || i >= b.size {
		return
	}
	b.int.SetBit(b.int, i, 0)
}

// Load returns if a bit at a specific index in the Bitset is set. Indices outside the
// Bitset return false.
func (b Bitset) Load(i int) bool {
	if i < 0 || i >= b.size {
		return false
	}
	return b.int.Bit(i) == 1
}

// Len returns the size of the Bitset.
func (b Bitset) Len() int {
	return b.size
}
