package main

import (
	"bufio"
	"demo/src/testsql"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math"
)

const LIM = 41

var fibs [LIM]uint64

func AddUpper() func(int) int {
	var n int = 100
	return func(i int) int {
		n = n + i
		return n

	}

}

type Node struct {
	le   *Node
	data interface{}
	ri   *Node
}

func NewNode(left, right *Node) *Node {
	return &Node{left, nil, right}
}

func (n *Node) SetData(data interface{}) {
	n.data = data
}

const sliceBytes = 24

type Buffer struct {
	endian binary.ByteOrder
	off    int
	buf    []byte
}

// Put32 appends uint32 to Buffer
func (b *Buffer) Put32(v uint32) {
	b.endian.PutUint32(b.buf[b.off:b.off+4], v)
	b.off += 4
}

// Get32 returns uint32
func (b *Buffer) Get32() uint32 {
	v := b.endian.Uint32(b.buf[b.off : b.off+4])
	b.off += 4
	return v
}

// Put64 appends uint64 to Buffer
func (b *Buffer) Put64(v uint64) {
	b.endian.PutUint64(b.buf[b.off:b.off+8], v)
	b.off += 8
}

// Bytes returns the bytes
func (b *Buffer) Bytes() []byte {
	return b.buf
}

func marshalSlice(pos uint32, id uint64, size, off, len uint32) []byte {
	w := &Buffer{binary.BigEndian, 0, make([]byte, sliceBytes)}
	w.Put32(pos)
	w.Put64(id)
	w.Put32(size)
	w.Put32(off)
	w.Put32(len)
	return w.Bytes()
}

type slice struct {
	id    uint64
	size  uint32
	off   uint32
	len   uint32
	pos   uint32
	left  *slice
	right *slice
}

func FromBuffer(buf []byte) *Buffer {
	return &Buffer{binary.BigEndian, 0, buf}
}

func ReadBuffer(buf []byte) *Buffer {
	return FromBuffer(buf)
}

// Get64 returns uint64
func (b *Buffer) Get64() uint64 {
	v := b.endian.Uint64(b.buf[b.off : b.off+8])
	b.off += 8
	return v
}

func (s *slice) read(buf []byte) {
	rb := ReadBuffer(buf)
	s.pos = rb.Get32()
	s.id = rb.Get64()
	s.size = rb.Get32()
	s.off = rb.Get32()
	s.len = rb.Get32()
}

func readSliceBuf(buf []byte) []*slice {
	if len(buf)%sliceBytes != 0 {
		fmt.Println("corrupt slices: len=%d", len(buf))
		return nil
	}
	nSlices := len(buf) / sliceBytes
	slices := make([]slice, nSlices)
	ss := make([]*slice, nSlices)
	for i := 0; i < len(buf); i += sliceBytes {
		s := &slices[i/sliceBytes]
		s.read(buf[i:])
		ss[i/sliceBytes] = s
	}
	return ss
}

type Slice struct {
	Id   uint64
	Size uint32
	Off  uint32
	Len  uint32
}

func newSlice(pos uint32, id uint64, cleng, off, len uint32) *slice {
	if len == 0 {
		return nil
	}
	s := &slice{}
	s.pos = pos
	s.id = id
	s.size = cleng
	s.off = off
	s.len = len
	s.left = nil
	s.right = nil
	return s
}

func (s *slice) cut(pos uint32) (left, right *slice) {
	if s == nil {
		return nil, nil
	}
	if pos <= s.pos {
		if s.left == nil {
			s.left = newSlice(pos, 0, 0, 0, s.pos-pos)
		}
		left, s.left = s.left.cut(pos)
		return left, s
	} else if pos < s.pos+s.len {
		l := pos - s.pos
		right = newSlice(pos, s.id, s.size, s.off+l, s.len-l)
		right.right = s.right
		s.len = l
		s.right = nil
		return s, right
	} else {
		if s.right == nil {
			s.right = newSlice(s.pos+s.len, 0, 0, 0, pos-s.pos-s.len)
		}
		s.right, right = s.right.cut(pos)
		return s, right
	}
}

func (s *slice) visit(f func(*slice)) {
	if s == nil {
		return
	}
	s.left.visit(f)
	right := s.right
	f(s) // s could be freed
	right.visit(f)
}

func buildSlice(ss []*slice) []Slice {
	var root *slice
	for i := range ss {
		s := new(slice)
		*s = *ss[i]
		var right *slice
		s.left, right = root.cut(s.pos)
		_, s.right = right.cut(s.pos + s.len)
		root = s
	}
	var pos uint32
	var chunk []Slice
	root.visit(func(s *slice) {
		if s.pos > pos {
			chunk = append(chunk, Slice{Size: s.pos - pos, Len: s.pos - pos})
			pos = s.pos
		}
		chunk = append(chunk, Slice{Id: s.id, Size: s.size, Off: s.off, Len: s.len})
		pos += s.len
	})
	return chunk
}

func main() {
	//root := NewNode(nil, nil)
	//root.SetData("root node")
	//an := NewNode(nil, nil)
	//an.SetData("left node")
	//bn := NewNode(nil, nil)
	//bn.SetData("right node")
	//root.le = an
	//root.ri = bn
	//fmt.Printf("%v\n", root)
	//
	//start := time.Now()
	//fmt.Println("Hello, world!")
	//var goos string = runtime.GOOS
	//fmt.Printf("The operating system is: %s\n", goos)
	//path := os.Getenv("PATH")
	//fmt.Printf("Path is %v\n", path)
	//
	//k := 6
	//switch k {
	//case 6:
	//	fmt.Println("was <=6")
	//	fallthrough
	//default:
	//	fmt.Println("default case")
	//
	//}
	//
	//f := AddUpper()
	//fmt.Println(f(1))
	//end := time.Now()
	//delta := end.Sub(start)
	//fmt.Printf("time: %s\n", delta)
	//
	//var result uint64 = 0
	//for i := 0; i < LIM; i++ {
	//	result = fibonacci(i)
	//	fmt.Println("fibonacci(%d) is: %d\n", i, result)
	//}

	//var buffer bytes.Buffer
	//for {
	//	if s, ok := getNextString(); ok {
	//		buffer.WriteString(s)
	//	} else {
	//		break
	//	}
	//}
	//
	//fmt.Print(buffer.String(), "\n")
	//
	//strconv.FormatFloat(v*2, "f', 2, 32")

	//var areaIntf Shaper
	//sq1 := new(Square)
	//sq1.side = 5
	//
	//areaIntf = sq1
	//if t, ok := areaIntf.(*Square); ok {
	//	fmt.Printf("The type of areaIntf is: %T\n", t)
	//}
	//
	//if u, ok := areaIntf.(*Circle); ok {
	//	fmt.Printf("The type of areaIntf is: %T\n", u)
	//} else {
	//	fmt.Println("areaIntf does not contain a variable of type Circle")
	//}
	//
	//switch t := areaIntf.(type) {
	//case *Square:
	//	fmt.Printf("Type Square %T with value %v\n", t, t)
	//case *Circle:
	//	fmt.Printf("Type Circle %T with value %v\n", t, t)
	//case nil:
	//	fmt.Printf("nil value: nothing to check?\n")
	//default:
	//	fmt.Printf("Unexpected type %T\n", t)
	//}

	//classifier(13, -14.3, nil)
	//
	//if sv, ok := sq1.(Shaper); ok {
	//	fmt.Printf("v implements String(): %s\n", sv.String())
	//
	//}

	//data := []int{74, 59, 238, -784, 9845, 959, 905, 0, 0, 42, 7586, -5467984, 7586}
	//a := sort.IntArray(data)
	//sort.Sort(a)
	////panic("fail")
	//fmt.Printf("The sorted array is: %v\n", a)
	//
	//test := Cars(data)
	//
	//fmt.Printf("test: %v\n", test)
	//
	//m := make(map[string]Cars1)
	//m["10"] = make([]*Car, 0)
	//ford := &Car{"1", "2", 3}
	//m["10"] = append(m["10"], ford)
	//
	//fmt.Printf("ford: %v, value: %v\n", m, m["10"][0])
	//
	//fmt.Println("please enter your full name: ")
	////fmt.Scanln(&firstName, &lastName)
	////fmt.Printf("Hi %s %s!\n", firstName, lastName)
	//
	//fmt.Sscanf(input1, format, &f1, &i1, &s1)
	//
	//fmt.Println("From the string we read: ", f1, i1, s1)
	//
	//inputReader = bufio.NewReader(os.Stdin)
	//fmt.Println("Please enter some input: ")
	//input, err = inputReader.ReadString('\n')
	//if err == nil {
	//	fmt.Printf("The input was: %s\n", input)
	//}
	//
	////fName := "/home/wjy/imagenet/test.tar"
	////var r *bufio.Reader
	////fi, err := os.Open(fName)
	////if err != nil {
	////	fmt.Fprintf(os.Stderr, "%v, Can't open %s: error: %s\n", os.Args[0], fName, err)
	////	os.Exit(1)
	////}
	////
	////fz, err := gzip.NewReader(fi)
	////if err != nil {
	////	r = bufio.NewReader(fi)
	////} else {
	////	r = bufio.NewReader(fz)
	////}
	////
	////for {
	////	line, err := r.ReadString('\n')
	////	if err != nil {
	////		fmt.Println("Done reading file")
	////		os.Exit(0)
	////	}
	////
	////	fmt.Println(line)
	////}
	//
	//flag.PrintDefaults()
	//flag.Parse()
	//var s string = ""
	//for i := 0; i < flag.NArg(); i++ {
	//	if i > 0 {
	//		s += " "
	//		if *NewLine { // -n is parsed, flag becomes true
	//			s += Newline
	//		}
	//	}
	//	s += flag.Arg(i)
	//}
	//os.Stdout.WriteString(s)
	//
	//suck(pump())
	//time.Sleep(1e9)
	//ch := make(chan int) // Create a new channel.
	//go generate(ch)      // Start generate() as a goroutine.
	//for {
	//	prime := <-ch
	//	fmt.Printf("prime: %d\n", prime)
	//	ch1 := make(chan int)
	//	go filter(ch, ch1, prime)
	//	ch = ch1
	//}
	//
	//time.Sleep(1e9)
	engine := testsql.Init()
	//engine.Logger().SetLevel(core.LOG_DEBUG)

	log.Println("delete chunk file: %v")
	var f = testsql.ChunkFile{Inode: 45}
	ok, err := engine.Get(&f)
	if err != nil {
		log.Println(err)
	}
	if !ok {
		log.Println(ok)
	}

	//if _, err := engine.Delete(&testsql.ChunkFile{Inode: f.Inode}); err != nil {
	//	log.Println(err)
	//}
	if ok {
		log.Println("update info")
		f.Name = []byte("data-set.tar.gz")
		count, err1 := engine.Cols("files", "name").Update(&f, &testsql.ChunkFile{Inode: f.Inode})
		log.Println("count: %d", count)
		if err1 != nil {
			log.Println(err1)
		}
	} else {
		log.Println("insert info")
		_, err = engine.Insert(f)
		if err != nil {
			log.Println(err)
		}
	}

	//
	//engine.Sync2(new(testsql.ChunkFile))
	//
	////testsql.CreateTableByEngine(engine)
	//
	//chunk := new(testsql.ChunkFile)
	//chunk.Id = 1
	//chunk.ChunkId = 1
	//chunk.Inode = 2
	//
	////buf := marshalSlice(0, 1, 67108864, 0, 67108864)
	//
	//chunk.Files = []string{"ILSVRC2012_test_00000001.JPEG", "ILSVRC2012_test_00000002.JPEG"}
	//chunk.Files = append(chunk.Files, "ILSVRC2012_test_00000003.JPEG")
	//
	//chunk.Name = []byte("archive.tar")
	//
	//cout, _ := engine.Insert(chunk)
	//
	//engine.Update(chunk)
	//
	//fmt.Println("count: %v", cout)
	//
	//getChunk := &testsql.ChunkFile{}
	//
	//engine.ID(1).Get(getChunk)
	//
	//fileList := getChunk.Files
	//
	//fmt.Println("get chunk: %v", fileList)

	//ss := readSliceBuf(getChunk.Files)

	//fmt.Println("ss: %v", getChunk.Files)

	//tar.TarFolder("/mnt/jfs2/imagenet_4M", "/mnt/jfs2/archive.tar")

	defer engine.Close()

}

func generate(ch chan int) {
	for i := 2; i < 20; i++ {
		ch <- i // Send 'i' to channel 'ch'.
		fmt.Printf("i: %d\n", i)
	}
}

// Copy the values from channel 'in' to channel 'out',
// removing those divisible by 'prime'.
func filter(in, out chan int, prime int) {
	for {
		i := <-in // Receive value of new variable 'i' from 'in'.
		fmt.Printf("filter i: %d\n", i)
		if i%prime != 0 {
			out <- i // Send 'i' to channel 'out'.
			fmt.Printf("out: %d\n", i)
		}
		fmt.Printf("prime: %d, i: %d\n", prime, i)
	}
}

func pump() chan int {
	ch := make(chan int)
	go func() {
		for i := 0; ; i++ {
			ch <- i

		}
	}()
	return ch
}

func suck(ch chan int) {
	go func() {
		for v := range ch {
			fmt.Println(v)
		}
	}()

}

var NewLine = flag.Bool("n", false, "print newline")

const (
	Space   = " "
	Newline = "\n"
)

var inputReader *bufio.Reader

var input string

var err error

var (
	firstName, lastName, s1 string
	i1                      int
	f1                      float32
	input1                  = "56.12 / 5212 / Go"
	format                  = "%f / %d / %s"
)

type Cars1 []*Car

type Car struct {
	Model        string
	Manufacturer string
	BuildYear    int
}

type Cars []int
type Stringer interface {
	String() string
}

func fibonacci(n int) (res uint64) {
	if fibs[n] != 0 {
		res = fibs[n]
		return
	}

	if n <= 1 {
		res = 1
	} else {
		res = fibonacci(n-1) + fibonacci(n-2)
	}
	fibs[n] = res
	return

}

type Square struct {
	side float32
}

type Circle struct {
	radius float32
}

type Shaper interface {
	Area() float32
}

func (sq *Square) Area() float32 {
	return sq.side * sq.side
}

func (ci *Circle) Area() float32 {
	return ci.radius * ci.radius * math.Pi

}

func classifier(items ...interface{}) {
	for i, x := range items {
		switch x.(type) {
		case bool:
			fmt.Printf("Param #%d is a bool\n", i)
		case float64:
			fmt.Printf("Param #%d is a float64\n", i)
		case int, int64:
			fmt.Printf("Param #%d is a int\n", i)
		case nil:
			fmt.Printf("Param #%d is a nil\n", i)
		case string:
			fmt.Printf("Param #%d is a string\n", i)
		default:
			fmt.Printf("Param #%d is unknown\n", i)

		}
	}
}
