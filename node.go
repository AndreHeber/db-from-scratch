package main

import (
	"bytes"
	"encoding/binary"
)

// layout of a node
// | type | nkeys |  pointers  |   offsets  | key-values | unused |
// |  2B  |   2B  | nkeys * 8B | nkeys * 2B |     ...    |        |

// | klen | vlen | key | val |
// |  2B  |  2B  | ... | ... |

// node types
const (
	BNODE_NODE = 1
	BNODE_LEAF = 2
)

// assert that a condition is true
func assert(cond bool) {
	if !cond {
		panic("assertion failed")
	}
}

type BTree struct {
	// pointer (a nonzero page number)
	root uint64
	// callbacks for managing on-disk pages
	get func(uint64) []byte // dereference a pointer
	new func([]byte) uint64 // allocate a new page
	del func(uint64)        // deallocate a page
}

type BNode []byte

// return the type of the node
func (node BNode) btype() uint16 {
	return binary.LittleEndian.Uint16(node[0:2])
}

// return the number of keys in the node
func (node BNode) nkeys() uint16 {
	return binary.LittleEndian.Uint16(node[2:4])
}

// set the type and the number of keys of the node
func (node BNode) setHeader(btype uint16, nkeys uint16) {
	binary.LittleEndian.PutUint16(node[0:2], btype)
	binary.LittleEndian.PutUint16(node[2:4], nkeys)
}

// return the pointer of the key at the given index
func (node BNode) getPtr(idx uint16) uint64 {
	assert(idx < node.nkeys())
	pos := HEADER + 8*idx
	return binary.LittleEndian.Uint64(node[pos:])
}

// set the pointer of the key at the given index
func (node BNode) setPtr(idx uint16, val uint64) {
	assert(idx < node.nkeys())
	pos := HEADER + 8*idx
	binary.LittleEndian.PutUint64(node[pos:], val)
}

// return the position of the offset of the key at the given index
func offsetPos(node BNode, idx uint16) uint16 {
	assert(1 <= idx && idx <= node.nkeys())
	return HEADER + 8*node.nkeys() + 2*(idx-1)
}

// return the offset of the key at the given index
func (node BNode) getOffset(idx uint16) uint16 {
	if idx == 0 {
		return 0
	}
	return binary.LittleEndian.Uint16(node[offsetPos(node, idx):])
}

// set the offset of the key at the given index
func (node BNode) setOffset(idx uint16, val uint16) {
	if idx == 0 {
		return
	}
	binary.LittleEndian.PutUint16(node[offsetPos(node, idx):], val)
}

// return the position of the key-value pair at the given index
func (node BNode) kvPos(idx uint16) uint16 {
	assert(idx <= node.nkeys())
	return HEADER + 8*node.nkeys() + 2*node.nkeys() + node.getOffset(idx)
}

// return the key of the key-value pair at the given index
func (node BNode) getKey(idx uint16) []byte {
	assert(idx < node.nkeys())
	pos := node.kvPos(idx)
	klen := binary.LittleEndian.Uint16(node[pos:])
	return node[pos+4:][:klen]
}

// return the value of the key-value pair at the given index
func (node BNode) getVal(idx uint16) []byte {
	assert(idx < node.nkeys())
	pos := node.kvPos(idx)
	klen := binary.LittleEndian.Uint16(node[pos:])
	vlen := binary.LittleEndian.Uint16(node[pos+2+klen:])
	return node[pos+4+klen:][:vlen]
}

// return the number of bytes of the node
func (node BNode) nbytes() uint16 {
	return node.kvPos(node.nkeys())
}

// returns the first kid node whose range intersects the key. (kid[i] <= key)
// TODO: binary search
func nodeLookupLE(node BNode, key []byte) uint16 {
	nkeys := node.nkeys()
	found := uint16(0)

	// the first key is a copy from the parent node,
	// thus it is always less than or equal to the key.
	for i := uint16(1); i < nkeys; i++ {
		cmp := bytes.Compare(node.getKey(i), key)
		if cmp <= 0 {
			found = i
		} else {
			break
		}
	}
	return found
}

// insert a key-value pair into a leaf node
func leafInsert(
	new BNode, old BNode, idx uint16,
	key []byte, val []byte,
) {
	new.setHeader(BNODE_LEAF, old.nkeys()+1)
	nodeAppendRange(new, old, 0, 0, idx)
	nodeAppendKV(new, idx, 0, key, val)
	nodeAppendRange(new, old, idx+1, idx, old.nkeys()-idx)
}

// update a key-value pair in a leaf node
func leafUpdate(
	new BNode, old BNode, idx uint16,
	key []byte, val []byte,
) {
	new.setHeader(BNODE_LEAF, old.nkeys())
	nodeAppendRange(new, old, 0, 0, idx)
	nodeAppendKV(new, idx, 0, key, val)
	nodeAppendRange(new, old, idx+1, idx+1, old.nkeys()-idx-1)
}

// append a range of key-value pairs from one node to another
func nodeAppendRange(
	new BNode, old BNode,
	dstNew uint16, srcOld uint16, count uint16,
) {
	for i := uint16(0); i < count; i++ {
		new.setPtr(dstNew+i, old.getPtr(srcOld+i))
	}
}

// append a key-value pair to a node
func nodeAppendKV(
	new BNode, idx uint16, ptr uint64,
	key []byte, val []byte,
) {
	new.setPtr(idx, ptr)
	pos := new.kvPos(idx)
	binary.LittleEndian.PutUint16(new[pos+0:], uint16(len(key)))
	binary.LittleEndian.PutUint16(new[pos+2:], uint16(len(val)))
	copy(new[pos+4:], key)
	copy(new[pos+4+uint16(len(key)):], val)
	// the offset of the next key
	new.setOffset(idx+1, new.getOffset(idx)+4+uint16((len(key)+len(val))))
}

// replace a link with one or multiple links
func nodeReplaceKidN(
	tree *BTree, new BNode, old BNode, idx uint16,
	kids ...BNode,
) {
	inc := uint16(len(kids))
	new.setHeader(BNODE_NODE, old.nkeys()+inc-1)
	nodeAppendRange(new, old, 0, 0, idx)
	for i, node := range kids {
		nodeAppendKV(new, idx+uint16(i), tree.new(node), node.getKey(0), nil)
		//                ^position      ^pointer        ^key            ^val
	}
	nodeAppendRange(new, old, idx+inc, idx+1, old.nkeys()-(idx+1))
}

// split a oversized node into 2 so that the 2nd node always fits on a page
func nodeSplit2(left BNode, right BNode, old BNode) {
	// find the split point
	split := old.nkeys() / 2
	// ensure the right node fits in a page
	for split < old.nkeys() {
		if HEADER+8+2+4+int(old.getOffset(old.nkeys())-old.getOffset(split)) <= BTREE_PAGE_SIZE {
			break
		}
		split++
	}
	assert(split < old.nkeys())

	// copy left half
	left.setHeader(old.btype(), split)
	nodeAppendRange(left, old, 0, 0, split)

	// copy right half
	right.setHeader(old.btype(), old.nkeys()-split)
	nodeAppendRange(right, old, 0, split, old.nkeys()-split)
}

// split a node if it's too big. the results are 1~3 nodes.
func nodeSplit3(old BNode) (uint16, [3]BNode) {
	if old.nbytes() <= BTREE_PAGE_SIZE {
		old = old[:BTREE_PAGE_SIZE]
		return 1, [3]BNode{old} // not split
	}
	left := BNode(make([]byte, 2*BTREE_PAGE_SIZE)) // might be split later
	right := BNode(make([]byte, BTREE_PAGE_SIZE))
	nodeSplit2(left, right, old)
	if left.nbytes() <= BTREE_PAGE_SIZE {
		left = left[:BTREE_PAGE_SIZE]
		return 2, [3]BNode{left, right} // 2 nodes
	}
	leftleft := BNode(make([]byte, BTREE_PAGE_SIZE))
	middle := BNode(make([]byte, BTREE_PAGE_SIZE))
	nodeSplit2(leftleft, middle, left)
	assert(leftleft.nbytes() <= BTREE_PAGE_SIZE)
	return 3, [3]BNode{leftleft, middle, right} // 3 nodes
}

// tree functions

// insert a KV into a node, the result might be split.
// the caller is responsible for deallocating the input node
// and splitting and allocating result nodes.
func treeInsert(tree *BTree, node BNode, key []byte, val []byte) BNode {
	// the result node.
	// it's allowed to be bigger than 1 page and will be split if so
	new := BNode(make([]byte, 2*BTREE_PAGE_SIZE))

	// where to insert the key?
	idx := nodeLookupLE(node, key)
	// act depending on the node type
	switch node.btype() {
	case BNODE_LEAF:
		// leaf, node.getKey(idx) <= key
		if bytes.Equal(key, node.getKey(idx)) {
			// found the key, update it.
			leafUpdate(new, node, idx, key, val)
		} else {
			// insert it after the position.
			leafInsert(new, node, idx+1, key, val)
		}
	case BNODE_NODE:
		// internal node, insert it to a kid node.
		nodeInsert(tree, new, node, idx, key, val)
	default:
		panic("bad node!")
	}
	return new
}

// part of the treeInsert(): KV insertion to an internal node
func nodeInsert(
	tree *BTree, new BNode, node BNode, idx uint16,
	key []byte, val []byte,
) {
	kptr := node.getPtr(idx)
	// recursive insertion to the kid node
	knode := treeInsert(tree, tree.get(kptr), key, val)
	// split the result
	nsplit, split := nodeSplit3(knode)
	// deallocate the kid node
	tree.del(kptr)
	// update the kid links
	nodeReplaceKidN(tree, new, node, idx, split[:nsplit]...)
}

// insert a KV into the tree
func (tree *BTree) Insert(key []byte, val []byte) error {
	// 1. check the length limit imposed by the node format
	if err := checkLimit(key, val); err != nil {
		return err // the only way for an update to fail
	}
	// 2. create the first node
	if tree.root == 0 {
		root := BNode(make([]byte, BTREE_PAGE_SIZE))
		// a dummy key, this makes the tree cover the whole key space.
		// thus a lookup can always find a containing node.
		nodeAppendKV(root, 0, 0, nil, nil)
		nodeAppendKV(root, 1, 0, key, val)
		tree.root = tree.new(root)
		return nil
	}
	// 3. insert the key
	node := treeInsert(tree, tree.get(tree.root), key, val)
	// 4. grow the tree if the root is split
	nsplit, split := nodeSplit3(node)
	tree.del(tree.root)
	if nsplit > 1 {
		root := BNode(make([]byte, BTREE_PAGE_SIZE))
		root.setHeader(BNODE_NODE, nsplit)
		for i, knode := range split[:nsplit] {
			ptr, key := tree.new(knode), knode.getKey(0)
			nodeAppendKV(root, uint16(i), ptr, key, nil)
		}
		tree.root = tree.new(root)
	} else {
		tree.root = tree.new(split[0])
	}
	return nil
}