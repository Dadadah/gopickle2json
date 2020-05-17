// Copyright 2020 NLP Odyssey Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gopickle

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/nlpodyssey/gopickle/types"
	"io"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
)

const HighestProtocol byte = 5

func Load(filename string) (interface{}, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	u := NewUnpickler(f)
	return u.Load()
}

func Loads(s string) (interface{}, error) {
	sr := strings.NewReader(s)
	u := NewUnpickler(sr)
	return u.Load()
}

type Unpickler struct {
	r              io.Reader
	proto          byte
	currentFrame   *bytes.Reader
	stack          []interface{}
	metaStack      [][]interface{}
	memo           map[int]interface{}
	FindClass      func(module, name string) (interface{}, error)
	PersistentLoad func(interface{}) (interface{}, error)
}

func NewUnpickler(r io.Reader) Unpickler {
	return Unpickler{
		r:    r,
		memo: make(map[int]interface{}),
	}
}

func (u *Unpickler) Load() (interface{}, error) {
	u.metaStack = make([][]interface{}, 0)
	u.stack = make([]interface{}, 0)
	u.proto = 0

	for {
		opcode, err := u.readOne()
		if err != nil {
			return nil, err
		}

		opFunc := dispatch[opcode]
		if opFunc == nil {
			return nil, fmt.Errorf("unknown opcode: 0x%x '%c'", opcode, opcode)
		}

		err = opFunc(u)
		if err != nil {
			if p, ok := err.(pickleStop); ok {
				return p.value, nil
			}
			return nil, err
		}
	}
}

type pickleStop struct{ value interface{} }

func (p pickleStop) Error() string { return "STOP" }

var _ error = pickleStop{}

func (u *Unpickler) findClass(module, name string) (interface{}, error) {
	switch module {
	case "collections":
		switch name {
		case "OrderedDict":
			return &types.OrderedDictClass{}, nil
		}

	case "__builtin__":
		switch name {
		case "object":
			return &types.ObjectClass{}, nil
		}
	case "copy_reg":
		switch name {
		case "_reconstructor":
			return &types.Reconstructor{}, nil
		}
	}
	if u.FindClass != nil {
		return u.FindClass(module, name)
	}
	return types.NewGenericClass(module, name), nil
}

func (u *Unpickler) read(n int) ([]byte, error) {
	buf := make([]byte, n)

	if u.currentFrame != nil {
		m, err := io.ReadFull(u.currentFrame, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, err
		}
		if m == 0 && n != 0 {
			u.currentFrame = nil
			m, err := io.ReadFull(u.r, buf)
			return buf[0:m], err
		}
		if m < n {
			return nil, fmt.Errorf("pickle exhausted before end of frame")
		}
		return buf[0:m], nil
	}

	m, err := io.ReadFull(u.r, buf)
	return buf[0:m], err
}

func (u *Unpickler) readOne() (byte, error) {
	buf, err := u.read(1)
	if err != nil {
		return 0, err
	}
	return buf[0], nil
}

func (u *Unpickler) readLine() ([]byte, error) {
	if u.currentFrame != nil {
		line, err := readLine(u.currentFrame)
		if err != nil {
			if err == io.EOF && len(line) == 0 {
				u.currentFrame = nil
				return readLine(u.r)
			}
			return nil, err
		}
		if len(line) == 0 {
			return nil, fmt.Errorf("readLine no data")
		}
		if line[len(line)-1] != '\n' {
			return nil, fmt.Errorf("pickle exhausted before end of frame")
		}
		return line, nil
	}
	return readLine(u.r)
}

func readLine(r io.Reader) (line []byte, err error) {
	line = make([]byte, 0, 32)
	b := make([]byte, 1)
	var n int
	for {
		n, err = r.Read(b)
		if n != 1 {
			return
		}
		line = append(line, b[0])
		if b[0] == '\n' || err != nil {
			return
		}
	}
}

// func (u *Unpickler) readinto() (interface{}, error) {
// }

func (u *Unpickler) loadFrame(frameSize int) error {
	buf := make([]byte, frameSize)
	if u.currentFrame != nil {
		n, err := (*u.currentFrame).Read(buf)
		if n > 0 || err == nil {
			return fmt.Errorf(
				"beginning of a new frame before end of current frame")
		}
	}
	_, err := io.ReadFull(u.r, buf)
	if err != nil {
		return err
	}
	u.currentFrame = bytes.NewReader(buf)
	return nil
}

func (u *Unpickler) append(element interface{}) {
	u.stack = append(u.stack, element)
}

func (u *Unpickler) stackLast() (interface{}, error) {
	if len(u.stack) == 0 {
		return nil, fmt.Errorf("the stack is empty")
	}
	return u.stack[len(u.stack)-1], nil
}

func (u *Unpickler) stackPop() (interface{}, error) {
	element, err := u.stackLast()
	if err != nil {
		return nil, err
	}
	u.stack = u.stack[:len(u.stack)-1]
	return element, nil
}

func (u *Unpickler) metaStackLast() ([]interface{}, error) {
	if len(u.metaStack) == 0 {
		return nil, fmt.Errorf("the meta stack is empty")
	}
	return u.metaStack[len(u.metaStack)-1], nil
}

func (u *Unpickler) metaStackPop() ([]interface{}, error) {
	element, err := u.metaStackLast()
	if err != nil {
		return nil, err
	}
	u.metaStack = u.metaStack[:len(u.metaStack)-1]
	return element, nil
}

// Returns a list of items pushed in the stack after last MARK instruction.
func (u *Unpickler) popMark() ([]interface{}, error) {
	items := u.stack
	newStack, err := u.metaStackPop()
	if err != nil {
		return nil, err
	}
	u.stack = newStack
	return items, nil
}

var dispatch [math.MaxUint8]func(*Unpickler) error

func init() {
	// Initialize `dispatch` assigning functions to opcodes

	// Protocol 0 and 1

	dispatch['('] = loadMark
	dispatch['.'] = loadStop
	// dispatch['0'] = opPop
	// dispatch['1'] = opPop_mark
	// dispatch['2'] = opDup
	dispatch['F'] = loadFloat
	dispatch['I'] = loadInt
	dispatch['J'] = loadBinInt
	dispatch['K'] = loadBinInt1
	dispatch['L'] = loadLong
	dispatch['M'] = loadBinInt2
	dispatch['N'] = loadNone
	dispatch['P'] = loadPersId
	dispatch['Q'] = loadBinPersId
	dispatch['R'] = loadReduce
	dispatch['S'] = loadString
	dispatch['T'] = loadBinString
	dispatch['U'] = loadShortBinString
	dispatch['V'] = loadUnicode
	dispatch['X'] = loadBinUnicode
	dispatch['a'] = loadAppend
	dispatch['b'] = loadBuild
	dispatch['c'] = loadGlobal
	dispatch['d'] = loadDict
	dispatch['}'] = loadEmptyDict
	dispatch['e'] = loadAppends
	// dispatch['g'] = opGet
	dispatch['h'] = loadBinGet
	// dispatch['i'] = opInst
	dispatch['j'] = loadLongBinGet
	dispatch['l'] = loadList
	dispatch[']'] = loadEmptyList
	// dispatch['o'] = opObj
	dispatch['p'] = loadPut
	dispatch['q'] = loadBinPut
	dispatch['r'] = loadLongBinPut
	dispatch['s'] = loadSetItem
	dispatch['t'] = loadTuple
	dispatch[')'] = loadEmptyTuple
	dispatch['u'] = loadSetItems
	dispatch['G'] = loadBinFloat

	// Protocol 2

	dispatch['\x80'] = loadProto
	dispatch['\x81'] = loadNewobj
	// dispatch['\x82'] = opExt1
	// dispatch['\x83'] = opExt2
	// dispatch['\x84'] = opExt4
	dispatch['\x85'] = loadTuple1
	dispatch['\x86'] = loadTuple2
	dispatch['\x87'] = loadTuple3
	dispatch['\x88'] = loadTrue
	dispatch['\x89'] = loadFalse
	dispatch['\x8a'] = loadLong1
	// dispatch['\x8b'] = opLong4

	// Protocol 3 (Python 3.x)

	dispatch['B'] = loadBinBytes
	dispatch['C'] = loadShortBinBytes

	// Protocol 4

	dispatch['\x8c'] = loadShortBinUnicode
	// dispatch['\x8d'] = opBinunicode8
	// dispatch['\x8e'] = opBinbytes8
	dispatch['\x8f'] = loadEmptySet
	dispatch['\x90'] = loadAddItems
	dispatch['\x91'] = loadFrozenSet
	// dispatch['\x92'] = opNewobj_ex
	dispatch['\x93'] = loadStackGlobal
	dispatch['\x94'] = loadMemoize
	dispatch['\x95'] = loadFrame

	// Protocol 5

	// dispatch['\x96'] = opBytearray8
	// dispatch['\x97'] = opNext_buffer
	// dispatch['\x98'] = opReadonly_buffer
}

// identify pickle protocol
func loadProto(u *Unpickler) error {
	proto, err := u.readOne()
	if err != nil {
		return err
	}
	if proto > HighestProtocol {
		return fmt.Errorf("unsupported pickle protocol: %d", proto)
	}
	u.proto = proto
	return nil
}

// indicate the beginning of a new frame
func loadFrame(u *Unpickler) error {
	buf, err := u.read(8)
	if err != nil {
		return err
	}
	frameSize := binary.LittleEndian.Uint64(buf)
	if frameSize > math.MaxInt64 {
		return fmt.Errorf("frame size > max int64: %d", frameSize)
	}
	return u.loadFrame(int(frameSize))
}

//push persistent object; id is taken from string arg
func loadPersId(u *Unpickler) error {
	line, err := u.readLine()
	if err != nil {
		return err
	}
	pid := string(line[:len(line)-1])
	result, err := u.PersistentLoad(pid)
	if err != nil {
		return err
	}
	u.append(result)
	return nil
}

// push persistent object; id is taken from stack
func loadBinPersId(u *Unpickler) error {
	if u.PersistentLoad == nil {
		return fmt.Errorf("unsupported persistent ID encountered")
	}
	pid, err := u.stackPop()
	if err != nil {
		return err
	}
	result, err := u.PersistentLoad(pid)
	if err != nil {
		return err
	}
	u.append(result)
	return nil
}

// push None (nil)
func loadNone(u *Unpickler) error {
	u.append(nil)
	return nil
}

// push False
func loadFalse(u *Unpickler) error {
	u.append(false)
	return nil
}

// push True
func loadTrue(u *Unpickler) error {
	u.append(true)
	return nil
}

// push integer or bool; decimal string argument
func loadInt(u *Unpickler) error {
	line, err := u.readLine()
	if err != nil {
		return err
	}
	data := string(line[:len(line)-1])
	if len(data) == 2 && data[0] == '0' && data[1] == '0' {
		u.append(false)
		return nil
	}
	if len(data) == 2 && data[0] == '0' && data[1] == '1' {
		u.append(true)
		return nil
	}
	i, err := strconv.Atoi(data)
	if err != nil {
		return err
	}
	u.append(i)
	return nil
}

// push four-byte signed int
func loadBinInt(u *Unpickler) error {
	buf, err := u.read(4)
	if err != nil {
		return err
	}
	u.append(decodeInt32(buf))
	return nil
}

// push 1-byte unsigned int
func loadBinInt1(u *Unpickler) error {
	i, err := u.readOne()
	if err != nil {
		return err
	}
	u.append(int(i))
	return nil
}

// push 2-byte unsigned int
func loadBinInt2(u *Unpickler) error {
	buf, err := u.read(2)
	if err != nil {
		return err
	}
	u.append(int(binary.LittleEndian.Uint16(buf)))
	return nil
}

// push long; decimal string argument
func loadLong(u *Unpickler) error {
	line, err := u.readLine()
	if err != nil {
		return err
	}
	sub := line[:len(line)-1]
	if len(sub) == 0 {
		return fmt.Errorf("invalid long data")
	}
	if sub[len(sub)-1] == 'L' {
		sub = sub[0 : len(sub)-1]
	}
	str := string(sub)
	i, err := strconv.ParseInt(str, 10, 64)

	if err != nil {
		if ne, isNe := err.(*strconv.NumError); isNe && ne.Err == strconv.ErrRange {
			bi, ok := new(big.Int).SetString(str, 10)
			if !ok {
				return fmt.Errorf("invalid long data")
			}
			u.append(bi)
			return nil
		}
		return err
	}
	u.append(int(i))
	return nil
}

// push long from < 256 bytes
func loadLong1(u *Unpickler) error {
	length, err := u.readOne()
	if err != nil {
		return err
	}
	buf, err := u.read(int(length))
	if err != nil {
		return err
	}

	msBitSet := buf[len(buf)-1]&0x80 != 0

	if len(buf) <= 8 {
		var ux, bitMask uint64
		_ = buf[len(buf)-1]
		for i := len(buf) - 1; i >= 0; i-- {
			ux = (ux << 8) | uint64(buf[i])
			bitMask = (bitMask << 8) | 0xFF
		}
		if msBitSet {
			u.append(-(int(^ux&bitMask) + 1))
		} else {
			u.append(int(ux))
		}
	} else {
		x := new(big.Int)
		_ = buf[len(buf)-1]
		for i := len(buf) - 1; i >= 0; i-- {
			x = x.Lsh(x, 8)
			if msBitSet {
				x = x.Or(x, big.NewInt(int64(^buf[i])))
			} else {
				x = x.Or(x, big.NewInt(int64(buf[i])))
			}
		}
		if msBitSet {
			x = x.Add(x, big.NewInt(1))
			x = x.Neg(x)
		}
		u.append(x)
	}
	return nil
}

// push really big long
// func opLong4(u *Unpickler) error {
// }

// push float object; decimal string argument
func loadFloat(u *Unpickler) error {
	line, err := u.readLine()
	if err != nil {
		return err
	}
	f, err := strconv.ParseFloat(string(line[:len(line)-1]), 64)
	if err != nil {
		return err
	}
	u.append(f)
	return nil
}

// push float; arg is 8-byte float encoding
func loadBinFloat(u *Unpickler) error {
	buf, err := u.read(8)
	if err != nil {
		return err
	}
	u.append(math.Float64frombits(binary.BigEndian.Uint64(buf)))
	return nil
}

// push string; NL-terminated string argument
func loadString(u *Unpickler) error {
	line, err := u.readLine()
	if err != nil {
		return err
	}
	data := line[:len(line)-1]
	// Strip outermost quotes
	if !isQuotedString(data) {
		return fmt.Errorf("the STRING opcode argument must be quoted")
	}
	data = data[1 : len(data)-1]
	// TODO: decode to string with the desired decoder
	u.append(string(data))
	return nil
}

func isQuotedString(b []byte) bool {
	return len(b) >= 2 && b[0] == b[len(b)-1] && (b[0] == '\'' || b[0] == '"')
}

// push string; counted binary string argument
func loadBinString(u *Unpickler) error {
	// Deprecated BINSTRING uses signed 32-bit length
	buf, err := u.read(4)
	if err != nil {
		return err
	}
	length := decodeInt32(buf)
	if length < 0 {
		return fmt.Errorf("BINSTRING pickle has negative byte count")
	}
	data, err := u.read(length)
	if err != nil {
		return err
	}
	// TODO: decode to string with the desired decoder
	u.append(string(data))
	return nil
}

// push bytes; counted binary string argument
func loadBinBytes(u *Unpickler) error {
	buf, err := u.read(4)
	if err != nil {
		return err
	}
	length := int(binary.LittleEndian.Uint32(buf))
	buf, err = u.read(length)
	if err != nil {
		return err
	}
	u.append(buf)
	return nil
}

// push Unicode string; raw-unicode-escaped'd argument
func loadUnicode(u *Unpickler) error {
	line, err := u.readLine()
	if err != nil {
		return err
	}
	u.append(string(line[:len(line)-1]))
	return nil
}

// push Unicode string; counted UTF-8 string argument
func loadBinUnicode(u *Unpickler) error {
	buf, err := u.read(4)
	if err != nil {
		return err
	}
	length := int(binary.LittleEndian.Uint32(buf))
	buf, err = u.read(length)
	if err != nil {
		return err
	}
	u.append(string(buf))
	return nil
}

// push very long string
// func opBinunicode8(u *Unpickler) error {
// }

// push very long bytes string
// func opBinbytes8(u *Unpickler) error {
// }

// push bytearray
// func opBytearray8(u *Unpickler) error {
// }

// push next out-of-band buffer
// func opNext_buffer(u *Unpickler) error {
// }

// make top of stack readonly
// func opReadonly_buffer(u *Unpickler) error {
// }

// push string; counted binary string argument < 256 bytes
func loadShortBinString(u *Unpickler) error {
	length, err := u.readOne()
	if err != nil {
		return err
	}
	data, err := u.read(int(length))
	if err != nil {
		return err
	}
	// TODO: decode to string with the desired decoder
	u.append(string(data))
	return nil
}

// push bytes; counted binary string argument < 256 bytes
func loadShortBinBytes(u *Unpickler) error {
	length, err := u.readOne()
	if err != nil {
		return err
	}
	buf, err := u.read(int(length))
	if err != nil {
		return err
	}
	u.append(buf)
	return nil
}

// push short string; UTF-8 length < 256 bytes
func loadShortBinUnicode(u *Unpickler) error {
	length, err := u.readOne()
	if err != nil {
		return err
	}
	buf, err := u.read(int(length))
	if err != nil {
		return err
	}
	u.append(string(buf))
	return nil
}

// build tuple from topmost stack items
func loadTuple(u *Unpickler) error {
	items, err := u.popMark()
	if err != nil {
		return err
	}
	u.append(types.NewTupleFromSlice(items))
	return nil
}

// push empty tuple
func loadEmptyTuple(u *Unpickler) error {
	u.append(types.NewTupleFromSlice([]interface{}{}))
	return nil
}

// build 1-tuple from stack top
func loadTuple1(u *Unpickler) error {
	value, err := u.stackPop()
	if err != nil {
		return err
	}
	u.append(types.NewTupleFromSlice([]interface{}{value}))
	return nil
}

// build 2-tuple from two topmost stack items
func loadTuple2(u *Unpickler) error {
	second, err := u.stackPop()
	if err != nil {
		return err
	}
	first, err := u.stackPop()
	if err != nil {
		return err
	}
	u.append(types.NewTupleFromSlice([]interface{}{first, second}))
	return nil
}

// build 3-tuple from three topmost stack items
func loadTuple3(u *Unpickler) error {
	third, err := u.stackPop()
	if err != nil {
		return err
	}
	second, err := u.stackPop()
	if err != nil {
		return err
	}
	first, err := u.stackPop()
	if err != nil {
		return err
	}
	u.append(types.NewTupleFromSlice([]interface{}{first, second, third}))
	return nil
}

// push empty list
func loadEmptyList(u *Unpickler) error {
	u.append(types.NewList())
	return nil
}

// push empty dict
func loadEmptyDict(u *Unpickler) error {
	u.append(types.NewDict())
	return nil
}

// push empty set on the stack
func loadEmptySet(u *Unpickler) error {
	u.append(types.NewSet())
	return nil
}

// build frozenset from topmost stack items
func loadFrozenSet(u *Unpickler) error {
	items, err := u.popMark()
	if err != nil {
		return err
	}
	u.append(types.NewFrozenSetFromSlice(items))
	return nil
}

// build list from topmost stack items
func loadList(u *Unpickler) error {
	items, err := u.popMark()
	if err != nil {
		return err
	}
	u.append(types.NewListFromSlice(items))
	return nil
}

// build a dict from stack items
func loadDict(u *Unpickler) error {
	items, err := u.popMark()
	if err != nil {
		return err
	}
	d := types.NewDict()
	itemsLen := len(items)
	for i := 0; i < itemsLen; i += 2 {
		d.Set(items[i], items[i+1])
	}
	u.append(d)
	return nil
}

// build & push class instance
// func opInst(u *Unpickler) error {
// }

// build & push class instance
// func opObj(u *Unpickler) error {
// }

// build object by applying cls.__new__ to argtuple
func loadNewobj(u *Unpickler) error {
	args, err := u.stackPop()
	if err != nil {
		return err
	}
	argsTuple, argsOk := args.(*types.Tuple)
	if !argsOk {
		return fmt.Errorf("REDUCE args must be *Tuple")
	}

	rawClass, err := u.stackPop()
	if err != nil {
		return err
	}
	class, classOk := rawClass.(types.PyNewable)
	if !classOk {
		return fmt.Errorf("NEWOBJ requires a PyNewable object")
	}

	result, err := class.PyNew(*argsTuple...)
	if err != nil {
		return err
	}
	u.append(result)
	return nil
}

// like NEWOBJ but work with keyword only arguments
// func opNewobj_ex(u *Unpickler) error {
// }

// push self.find_class(modname, name); 2 string args
func loadGlobal(u *Unpickler) error {
	line, err := u.readLine() // TODO: deode UTF-8?
	if err != nil {
		return err
	}
	module := string(line[0 : len(line)-1])

	line, err = u.readLine() // TODO: deode UTF-8?
	if err != nil {
		return err
	}
	name := string(line[0 : len(line)-1])

	class, err := u.findClass(module, name)
	if err != nil {
		return err
	}
	u.append(class)
	return nil
}

// same as GLOBAL but using names on the stacks
func loadStackGlobal(u *Unpickler) error {
	rawName, err := u.stackPop()
	if err != nil {
		return err
	}
	name, nameOk := rawName.(string)
	if !nameOk {
		return fmt.Errorf("STACK_GLOBAL requires str name: %#v", rawName)
	}

	rawModule, err := u.stackPop()
	if err != nil {
		return err
	}
	module, moduleOk := rawModule.(string)
	if !moduleOk {
		return fmt.Errorf("STACK_GLOBAL requires str module: %#v", rawModule)
	}

	class, err := u.findClass(module, name)
	if err != nil {
		return err
	}
	u.append(class)
	return nil
}

// push object from extension registry; 1-byte index
// func opExt1(u *Unpickler) error {
// }

// ditto, but 2-byte index
// func opExt2(u *Unpickler) error {
// }

// ditto, but 4-byte index
// func opExt4(u *Unpickler) error {
// }

// apply callable to argtuple, both on stack
func loadReduce(u *Unpickler) error {
	args, err := u.stackPop()
	if err != nil {
		return err
	}
	argsTuple, argsOk := args.(*types.Tuple)
	if !argsOk {
		return fmt.Errorf("REDUCE args must be *Tuple")
	}

	function, err := u.stackPop()
	if err != nil {
		return err
	}
	callable, callableOk := function.(types.Callable)
	if !callableOk {
		return fmt.Errorf("REDUCE requires a Callable object: %#v", function)
	}

	result, err := callable.Call(*argsTuple...)
	if err != nil {
		return err
	}
	u.append(result)
	return nil
}

// discard topmost stack item
// func opPop(u *Unpickler) error {
// }

// discard stack top through topmost markobject
// func opPop_mark(u *Unpickler) error {
// }

// duplicate top stack item
// func opDup(u *Unpickler) error {
// }

// push item from memo on stack; index is string arg
// func opGet(u *Unpickler) error {
// }

// push item from memo on stack; index is 1-byte arg
func loadBinGet(u *Unpickler) error {
	i, err := u.readOne()
	if err != nil {
		return err
	}
	u.append(u.memo[int(i)])
	return nil
}

// push item from memo on stack; index is 4-byte arg
func loadLongBinGet(u *Unpickler) error {
	buf, err := u.read(4)
	if err != nil {
		return err
	}
	i := int(binary.LittleEndian.Uint32(buf))
	u.append(u.memo[i])
	return nil
}

// store stack top in memo; index is string arg
func loadPut(u *Unpickler) error {
	line, err := u.readLine()
	if err != nil {
		return err
	}
	i, err := strconv.Atoi(string(line[:len(line)-1]))
	if err != nil {
		return err
	}
	if i < 0 {
		return fmt.Errorf("negative PUT argument")
	}
	u.memo[i], err = u.stackLast()
	return err
}

// store stack top in memo; index is 1-byte arg
func loadBinPut(u *Unpickler) error {
	i, err := u.readOne()
	if err != nil {
		return err
	}
	u.memo[int(i)], err = u.stackLast()
	return err
}

// store stack top in memo; index is 4-byte arg
func loadLongBinPut(u *Unpickler) error {
	buf, err := u.read(4)
	if err != nil {
		return err
	}
	i := int(binary.LittleEndian.Uint32(buf))
	u.memo[i], err = u.stackLast()
	return err
}

// store top of the stack in memo
func loadMemoize(u *Unpickler) error {
	value, err := u.stackLast()
	if err != nil {
		return err
	}
	u.memo[len(u.memo)] = value
	return nil
}

// append stack top to list below it
func loadAppend(u *Unpickler) error {
	value, err := u.stackPop()
	if err != nil {
		return err
	}
	obj, err := u.stackPop()
	if err != nil {
		return err
	}
	list, listOk := obj.(types.ListAppender)
	if !listOk {
		return fmt.Errorf("APPEND requires ListAppender")
	}
	list.Append(value)
	u.append(list)
	return nil
}

// extend list on stack by topmost stack slice
func loadAppends(u *Unpickler) error {
	items, err := u.popMark()
	if err != nil {
		return err
	}
	obj, err := u.stackPop()
	if err != nil {
		return err
	}
	list, listOk := obj.(types.ListAppender)
	if !listOk {
		return fmt.Errorf("APPEND requires List")
	}
	for _, item := range items {
		list.Append(item)
	}
	u.append(list)
	return nil
}

// add key+value pair to dict
func loadSetItem(u *Unpickler) error {
	value, err := u.stackPop()
	if err != nil {
		return err
	}
	key, err := u.stackPop()
	if err != nil {
		return err
	}
	obj, err := u.stackLast()
	if err != nil {
		return err
	}
	dict, dictOk := obj.(types.DictSetter)
	if !dictOk {
		return fmt.Errorf("SETITEM requires DictSetter")
	}
	dict.Set(key, value)
	return nil
}

// modify dict by adding topmost key+value pairs
func loadSetItems(u *Unpickler) error {
	items, err := u.popMark()
	if err != nil {
		return err
	}
	obj, err := u.stackPop()
	if err != nil {
		return err
	}
	dict, dictOk := obj.(types.DictSetter)
	if !dictOk {
		return fmt.Errorf("SETITEMS requires DictSetter")
	}
	itemsLen := len(items)
	for i := 0; i < itemsLen; i += 2 {
		dict.Set(items[i], items[i+1])
	}
	u.append(dict)
	return nil
}

// modify set by adding topmost stack items
func loadAddItems(u *Unpickler) error {
	items, err := u.popMark()
	if err != nil {
		return err
	}
	obj, err := u.stackPop()
	if err != nil {
		return err
	}
	set, setOk := obj.(types.SetAdder)
	if !setOk {
		return fmt.Errorf("ADDITEMS requires SetAdder")
	}
	for _, item := range items {
		set.Add(item)
	}
	u.append(set)
	return nil
}

// call __setstate__ or __dict__.update()
func loadBuild(u *Unpickler) error {
	state, err := u.stackPop()
	if err != nil {
		return err
	}
	inst, err := u.stackLast()
	if err != nil {
		return err
	}
	if obj, ok := inst.(types.PyStateSettable); ok {
		return obj.PySetState(state)
	}

	var slotState interface{}
	if tuple, ok := state.(*types.Tuple); ok && tuple.Len() == 2 {
		state = tuple.Get(0)
		slotState = tuple.Get(1)
	}

	if stateDict, ok := state.(*types.Dict); ok {
		instPds, instPdsOk := inst.(types.PyDictSettable)
		if !instPdsOk {
			return fmt.Errorf(
				"BUILD requires a PyDictSettable instance: %#v", inst)
		}
		for k, v := range *stateDict {
			err := instPds.PyDictSet(k, v)
			if err != nil {
				return err
			}
		}
	}

	if slotStateDict, ok := slotState.(*types.Dict); ok {
		instSa, instOk := inst.(types.PyAttrSettable)
		if !instOk {
			return fmt.Errorf(
				"BUILD requires a PyAttrSettable instance: %#v", inst)
		}
		for k, v := range *slotStateDict {
			sk, keyOk := k.(string)
			if !keyOk {
				return fmt.Errorf("BUILD requires string slot state keys")
			}
			instSa.PySetAttr(sk, v)
		}
	}

	return nil
}

// push special markobject on stack
func loadMark(u *Unpickler) error {
	u.metaStack = append(u.metaStack, u.stack)
	u.stack = make([]interface{}, 0)
	return nil
}

// every pickle ends with STOP
func loadStop(u *Unpickler) error {
	value, err := u.stackPop()
	if err != nil {
		return err
	}
	return pickleStop{value: value}
}

func decodeInt32(b []byte) int {
	ux := binary.LittleEndian.Uint32(b)
	x := int(ux)
	if b[3]&0x80 != 0 {
		x = -(int(^ux) + 1)
	}
	return x
}