package main

import (
	"archive/tar"
	"bufio"
	"demo/src/cmd"
	"demo/src/testsql"
	"demo/src/utils"
	"encoding/binary"
	"flag"
	"fmt"
	"github.com/google/gops/agent"
	"github.com/pyroscope-io/client/pyroscope"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
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

type SessionInfo struct {
	Version    string
	HostName   string
	MountPoint string
	ProcessID  int
}

//type Slice struct {
//	Id   uint64
//	Size uint32
//	Off  uint32
//	Len  uint32
//}

var (
	mutex sync.Mutex
	count uint64
)

type Ino uint64

// Entry is an entry inside a directory.
type Entry struct {
	Inode Ino
	Name  []byte
	Attr  *Attr
}

// Attr represents attributes of a node.
type Attr struct {
	Flags     uint8  // reserved flags
	Typ       uint8  // type of a node
	Mode      uint16 // permission mode
	Uid       uint32 // owner id
	Gid       uint32 // group id of owner
	Rdev      uint32 // device number
	Atime     int64  // last access time
	Mtime     int64  // last modified time
	Ctime     int64  // last change time for meta
	Atimensec uint32 // nanosecond part of atime
	Mtimensec uint32 // nanosecond part of mtime
	Ctimensec uint32 // nanosecond part of ctime
	Nlink     uint32 // number of links (sub-directories or hardlinks)
	Length    uint64 // length of regular file

	Parent    Ino  // inode of parent; 0 means tracked by parentKey (for hardlinks)
	Full      bool // the attributes are completed or not
	KeepCache bool // whether to keep the cached page or not
}

func parseAttr(n *testsql.Node, attr *Attr) {
	if attr == nil || n == nil {
		return
	}
	attr.Typ = n.Type
	attr.Mode = n.Mode
	attr.Flags = n.Flags
	attr.Uid = n.Uid
	attr.Gid = n.Gid
	attr.Atime = n.Atime / 1e6
	attr.Atimensec = uint32(n.Atime % 1e6 * 1000)
	attr.Mtime = n.Mtime / 1e6
	attr.Mtimensec = uint32(n.Mtime % 1e6 * 1000)
	attr.Ctime = n.Ctime / 1e6
	attr.Ctimensec = uint32(n.Ctime % 1e6 * 1000)
	attr.Nlink = n.Nlink
	attr.Length = n.Length
	attr.Rdev = n.Rdev
	attr.Parent = Ino(n.Parent)
	attr.Full = true
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

	//log.Println(filepath.Glob("/mnt/jfs/.*.JPEG"))
	filePath := "/mnt/jfs2/pack/imagenet_4M_1"
	if filePath == "" {
		filePath, err = os.Getwd()
		if err != nil {
			log.Println(err)
		}
	}

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		log.Println(err)
	}

	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		log.Println("failed to get inode")
	}

	inode := stat.Ino

	engine := testsql.Init()

	var nodes []testsql.NamedNode
	s := engine.NewSession()
	if fileInfo.IsDir() {
		s = engine.Table(&testsql.Edge{})
		s = s.Join("INNER", &testsql.ChunkFile{}, "jfs_edge.inode = jfs_chunk_file.inode")
		if err := s.Find(&nodes, &testsql.Edge{Parent: testsql.Ino(inode)}); err != nil {
			log.Println(err)
		}
	} else {
		s = engine.Table(&testsql.ChunkFile{})
		if err := s.Find(&nodes, &testsql.ChunkFile{Inode: testsql.Ino(inode)}); err != nil {
			log.Println(err)
		}
	}

	plus := 1
	if plus != 0 {
		//s = s.Join("INNER", &testsql.Node{}, "jfs_edge.inode=jfs_node.inode").Join("LEFT", &testsql.ChunkFile{}, "jfs_edge.inode = jfs_chunk_file.inode")

	}

	limit := -1
	if limit > 0 {
		s = s.Limit(limit, 0)
	}

	log.Printf("nodes: %v", nodes)
	//var entries *[]*Entry
	//entries := []*Entry{
	//	{
	//		Inode: Ino(inode),
	//		Name:  []byte("."),
	//		Attr:  &Attr{Typ: 2},
	//	},
	//}
	//entries = append(entries, &Entry{
	//	Inode: Ino(inode),
	//	Name:  []byte(".."),
	//	Attr:  &Attr{Typ: 2},
	//})

	var fileList []string
	for _, n := range nodes {
		log.Printf("nodes: %v", n.Name)
		if len(n.Name) == 0 {
			logger.Errorf("Corrupt entry with empty name: inode %d parent %d", inode)
			continue
		}

		fileList = append(fileList, n.Files...)

		//if n.Files != nil && len(n.Files) > 0 {
		//	for _, file := range n.Files {
		//		log.Println(html.EscapeString(strings.ReplaceAll(file, "/", "|")))
		//		entry := &Entry{
		//			Inode: Ino(n.Node.Inode),
		//			Name:  []byte(html.EscapeString(strings.ReplaceAll(file, "/", "|"))),
		//			Attr:  &Attr{},
		//		}
		//		if plus != 0 {
		//			parseAttr(&n.Node, entry.Attr)
		//		} else {
		//			entry.Attr.Typ = n.Type
		//		}
		//
		//		entries = append(entries, entry)
		//	}
		//	continue
		//}
		//
		//entry := &Entry{
		//	Inode: Ino(n.Node.Inode),
		//	Name:  n.Name,
		//	Attr:  &Attr{},
		//}
		//if plus != 0 {
		//	parseAttr(&n.Node, entry.Attr)
		//} else {
		//	entry.Attr.Typ = n.Type
		//}

		//entries = append(entries, entry)

	}
	log.Println(fileList)

	displayFileTree(fileList)

	//tree := createTree(fileList)
	//printTree(tree, "")

	//log.Printf("entries: %v, %v", entries, string(entries[1].Name))

	//
	//ck := testsql.Chunk{Inode: 129, Indx: 0}
	//ok, _ := engine.MustCols("indx").Get(&ck)
	//cs := buildSlice(readSliceBuf(ck.Slices))
	//
	//if ok {
	//	for _, v := range cs {
	//		log.Println(v)
	//	}
	//} else {
	//	log.Println("none")
	//}

	//var names []testsql.ChunkFile

	//engine.Sync2(&testsql.ChunkFile{})
	//
	//err := engine.Table((&testsql.ChunkFile{}).TableName()).Find(&names, testsql.ChunkFile{Name: []byte("tar2631416604")})
	//if err != nil {
	//	fmt.Println(err)
	//	return
	//}
	//
	////for i, name := range names {
	////	log.Printf("%v, %v", i, name)
	////}
	//
	//log.Println(names)
	//
	//session := &testsql.Session2{}
	//ok, err := engine.Get(session)
	//if err != nil {
	//	log.Println(err)
	//}
	//if !ok {
	//	log.Println(err)
	//}
	//
	//info := &SessionInfo{}
	//json.Unmarshal(session.Info, &info)
	//log.Println(info.MountPoint, info.ProcessID)

	//list := []string{"imagenet_4M/imagenet_1/ILSVRC2012_test_00000006.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000007.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000008.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000009.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000010.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000011.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000012.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000013.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000014.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000015.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000016.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000017.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000018.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000019.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000020.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000021.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000022.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000023.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000024.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000025.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000026.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000027.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000028.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000029.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000030.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000031.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000032.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000033.JPEG", "imagenet_4M/imagenet_1/ILSVRC2012_test_00000034.JPEG"}
	//
	//buf, err := json.Marshal(&list)
	//if err != nil {
	//	panic(err) // marshal SessionInfo should never fail
	//}
	//
	//chunk := testsql.ChunkFile{}
	//chunk.Files = buf
	//chunk.Inode = 1
	//chunk.ChunkId = 1
	//engine.Insert(&chunk)

	//
	//count, err1 := engine.Cols("files").Update(&names, &testsql.ChunkFile{Inode: f.Inode})
	//log.Println("count: %d", count)
	//if err1 != nil {
	//	log.Println(err1)
	//}

	////engine.Logger().SetLevel(core.LOG_DEBUG)
	//
	//log.Println("delete chunk file: %v")
	//var f = testsql.ChunkFile{Name: []byte("tar1943691686")}
	//ok, err := engine.Get(&f)
	//if err != nil {
	//	log.Println(err)
	//}
	//if !ok {
	//	log.Println(ok)
	//}
	//
	//log.Println("f: %v", f)

	//
	////if _, err := engine.Delete(&testsql.ChunkFile{Inode: f.Inode}); err != nil {
	////	log.Println(err)
	////}
	//if ok {
	//	log.Println("update info")
	//f.Files = []string{"data-set.tar.gz"}
	//count, err1 := engine.Cols("files").Update(&f, &testsql.ChunkFile{Inode: f.Inode})
	//log.Println("count: %d", count)
	//if err1 != nil {
	//	log.Println(err1)
	//}
	//} else {
	//	log.Println("insert info")
	//	_, err = engine.Insert(f)
	//	if err != nil {
	//		log.Println(err)
	//	}
	//}

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

	//defer engine.Close()
	//var language string
	//app := &cli.App{
	//	UseShortOptionHandling: true,
	//	Commands: []*cli.Command{
	//		cmd.CmdPack(),
	//		{
	//			Name:  "short",
	//			Usage: "complete a task on the list",
	//			Flags: []cli.Flag{
	//				&cli.BoolFlag{Name: "serve", Aliases: []string{"s"}},
	//				&cli.BoolFlag{Name: "option", Aliases: []string{"o"}},
	//				&cli.BoolFlag{Name: "message", Aliases: []string{"m"}},
	//			},
	//			Action: func(c *cli.Context) error {
	//				log.Println("serve", c.Bool("serve"))
	//				log.Println("option", c.Bool("option"))
	//				log.Println("message", c.Bool("message"))
	//				return nil
	//
	//			},
	//		},
	//		{
	//			Name:     "add",
	//			Category: "template",
	//			Aliases:  []string{"a"},
	//			Usage:    "add a task to the list",
	//			Action: func(c *cli.Context) error {
	//				log.Println("added task: ", c.Args().First())
	//				return nil
	//
	//			},
	//		},
	//
	//		{
	//			Name:    "template",
	//			Aliases: []string{"t"},
	//			Usage:   "add a new template",
	//			Subcommands: []*cli.Command{
	//				{
	//					Name:  "add",
	//					Usage: "add a new template",
	//					Action: func(c *cli.Context) error {
	//						log.Println("new task template: ", c.Args().First())
	//						return nil
	//
	//					},
	//				},
	//
	//				{
	//					Name:  "remove",
	//					Usage: "remove an existing template",
	//					Action: func(c *cli.Context) error {
	//						fmt.Println("removed task template: ", c.Args().First())
	//						return nil
	//					},
	//				},
	//			},
	//		},
	//	},
	//	Flags: []cli.Flag{
	//		&cli.StringFlag{
	//			Name:        "lang",
	//			Aliases:     []string{"language", "l"},
	//			Value:       "english",
	//			Usage:       "language for the greeting `FILE`",
	//			Destination: &language,
	//			EnvVars:     []string{"APP_LANG", "SYSTEM_LANG"},
	//			FilePath:    "../lang.txt",
	//			Required:    true,
	//			DefaultText: "chinese",
	//		},
	//	},
	//	Name:  "hello",
	//	Usage: "hello world example!",
	//	Action: func(c *cli.Context) error {
	//		name := "world"
	//		if c.NArg() > 0 {
	//			name = c.Args().Get(0)
	//		}
	//
	//		//if c.String("lang") == "english" {
	//		if language == "english" {
	//			log.Println("hello ", name)
	//		} else {
	//			log.Println("您好 ", name)
	//		}
	//
	//		for i := 0; i < c.NArg(); i++ {
	//			log.Println("%d: %s\n", i+1, c.Args().Get(i))
	//
	//		}
	//		log.Println("hello world!")
	//		return nil
	//	},
	//}

	/************pack cmd*************/

	app := &cli.App{
		Name:                 "juicefs",
		Usage:                "A POSIX file system built on Redis and object storage.",
		Version:              "v1.0",
		Copyright:            "Apache License 2.0",
		HideHelpCommand:      true,
		EnableBashCompletion: true,
		//Flags:                globalFlags(),
		Commands: []*cli.Command{
			cmd.CmdPack(),
		},
	}

	test1 := app.Run(os.Args)
	if test1 != nil {
		log.Fatal(test1)
	}

	//var wg sync.WaitGroup
	////var mutex sync.Mutex
	////var x int
	////var channel chan int
	//
	////var count cmd.Counter
	//// 启动 100 个 goroutine 来自增 x 的值
	//for i := 0; i < 100; i++ {
	//	wg.Add(1)
	//	go func() {
	//		defer wg.Done()
	//		defer mutex.Unlock()
	//		mutex.Lock()
	//		log.Println(count)
	//		count++
	//		//log.Println(count.Get())
	//		//count.Increment()
	//	}()
	//}
	//
	//// 等待所有 goroutine 执行完毕
	//// 这里使用了一个简单的时间延迟，实际使用中可能需要更好的方式
	//
	//wg.Wait()
	//log.Println(count)
	//
	//// Open the tar file for reading.
	//log.Println("ext: %v", filepath.Ext("/mnt/jfs2/tar1958319355"))
	//file, test1 := os.Open("/mnt/jfs2/imagenet_4M/ILSVRC2012_test_00000001.JPEG")
	//if test1 != nil {
	//	panic(test1)
	//}
	//defer file.Close()
	//
	//// Create a new tar reader.
	//reader := tar.NewReader(file)
	//
	//// Create an empty slice to hold the file names.
	//var fileNames []string
	//
	//// Iterate through the files in the tar archive.
	//for {
	//	header, err := reader.Next()
	//	if err == io.EOF {
	//		// End of tar archive.
	//		break
	//	}
	//
	//	if err != nil {
	//		fileNames = append(fileNames, filepath.Base("/mnt/jfs2/imagenet_4M/ILSVRC2012_test_00000001.JPEG"))
	//		break
	//	}
	//
	//	fileNames = append(fileNames, header.Name)
	//}
	//
	//// Print the file names.
	//fmt.Println(fileNames)

	//filePath := "/mnt/jfs/imagenet_4M/imagenet_1/ILSVRC2012_test_00000001.JPEG/"
	//
	//log.Println(filepath.Clean(filePath))
	//
	//log.Println(filepath.Rel(filepath.Dir("/mnt/jfs/imagenet_4M"), filePath))
	//
	//log.Println(strings.TrimPrefix(filePath, filepath.Join(filepath.Dir("/mnt/jfs/imagenet_4M"), "/")))

	//listInfos, err := os.ReadDir("/mnt/jfs2")
	//if err != nil {
	//	log.Println(err)
	//}
	//
	//for _, file := range listInfos {
	//	log.Println(file.Name())
	//}

	//// Define the directory path to extract file names from
	//dirPath := "/mnt/jfs/imagenet_4M"
	//// create a wait group to wait for all workers to finish
	//var wg sync.WaitGroup
	//
	//// create a channel to receive file paths
	//filePaths := make(chan string)
	//
	//// create a channel to receive arrays of file paths
	//filePathArrays := make(chan []string)
	//
	//// create a channel to signal when all workers have finished
	//done := make(chan bool)
	//
	//// start the workers
	//for i := 0; i < numWorkers; i++ {
	//	wg.Add(1)
	//	go worker(filePathArrays, &wg)
	//}
	//
	//// start the file path extractor
	//go extractFilePaths(dirPath, filePaths)
	//
	//// create a slice to hold file paths
	//var filePathSlice []string
	//
	//// create a variable to hold the total size of the files in the slice
	//var totalSize int64
	//
	//// create a ticker to periodically check the size of the slice
	//ticker := time.NewTicker(time.Second)
	//
	//// loop over the file paths received from the extractor
	//for filePath := range filePaths {
	//	// get the size of the file
	//	fileInfo, err := os.Stat(filePath)
	//	if err != nil {
	//		log.Printf("Error getting file info for %s: %s", filePath, err)
	//		continue
	//	}
	//	fileSize := fileInfo.Size()
	//
	//	// if adding the file would exceed the max size, send the slice to the workers
	//	if totalSize+fileSize > maxFileSize {
	//		// send the slice to the workers
	//		filePathArrays <- filePathSlice
	//
	//		// create a new slice to hold file paths
	//		filePathSlice = []string{filePath}
	//
	//		// reset the total size
	//		totalSize = fileSize
	//	} else {
	//		// add the file path to the slice
	//		filePathSlice = append(filePathSlice, filePath)
	//
	//		// add the file size to the total size
	//		totalSize += fileSize
	//	}
	//
	//	// check if the ticker has ticked
	//	select {
	//	case <-ticker.C:
	//		// do nothing
	//	default:
	//		// do nothing
	//	}
	//}
	//
	//// send the final slice to the workers
	//filePathArrays <- filePathSlice
	//
	//// close the file path arrays channel
	//close(filePathArrays)
	//
	//// wait for all workers to finish
	//go func() {
	//	wg.Wait()
	//	done <- true
	//}()
	//cd
	//// wait for all workers to finish or for a timeout
	//select {
	//case <-done:
	//	fmt.Println("All workers finished")
	//case <-time.After(10 * time.Second):
	//	fmt.Println("Timeout waiting for workers to finish")
	//}

	//// create a wait group to wait for all workers to finish
	//var wg sync.WaitGroup
	//
	//// create a channel to receive file paths
	//filePaths := make(chan string)
	//
	//// create a pool of workers
	//for i := 0; i < maxWorkers; i++ {
	//	wg.Add(1)
	//	go func() {
	//		defer wg.Done()
	//		for filePath := range filePaths {
	//			// create tar file
	//			err := createTar(filePath)
	//			if err != nil {
	//				log.Printf("error creating tar file for %s: %v", filePath, err)
	//			}
	//		}
	//	}()
	//}
	//
	//// get all file paths in the directory
	////dirPath := "/c:/Users/wangjiyu/Desktop"
	//filePathsList, err := getAllFilePaths(dirPath)
	//if err != nil {
	//	log.Fatalf("error getting file paths: %v", err)
	//}
	//
	//// create a slice to hold file paths to be processed
	//var filePathsSlice []string
	//
	//// iterate over all file paths and add them to the slice
	//for _, filePath := range filePathsList {
	//	// get file info
	//	fileInfo, err := os.Stat(filePath)
	//	if err != nil {
	//		log.Printf("error getting file info for %s: %v", filePath, err)
	//		continue
	//	}
	//
	//	// check if file size is greater than maxFileSize
	//	if fileInfo.Size() > maxFileSize {
	//		log.Printf("file %s is too large to process", filePath)
	//		continue
	//	}
	//
	//	// add file path to slice
	//	filePathsSlice = append(filePathsSlice, filePath)
	//
	//	// check if slice size is greater than maxFileSize
	//	if getSliceSize(filePathsSlice) > maxFileSize {
	//		// add slice to work queue
	//		filePathsSliceCopy := make([]string, len(filePathsSlice))
	//		copy(filePathsSliceCopy, filePathsSlice)
	//		filePaths <- filePathsSliceCopy
	//
	//		// reset slice
	//		filePathsSlice = nil
	//	}
	//}
	//
	//// add remaining files to work queue
	//if len(filePathsSlice) > 0 {
	//	filePathsSliceCopy := make([]string, len(filePathsSlice))
	//	copy(filePathsSliceCopy, filePathsSlice)
	//	filePaths <- filePathsSliceCopy
	//}
	//
	//// close the channel to signal that all files have been added to the work queue
	//close(filePaths)
	//
	//// wait for all workers to finish
	//wg.Wait()

	//var (
	//	wg       sync.WaitGroup
	//	queue    = make(chan []string, maxQueueLen)
	//	fileSize int64
	//)
	//
	//// Walk through the directory and add file names to the array
	//fileList := make([]string, 0)
	//err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
	//	if err != nil {
	//		return err
	//	}
	//	if !info.IsDir() {
	//		fileList = append(fileList, path)
	//		atomic.AddInt64(&fileSize, info.Size())
	//	}
	//	return nil
	//})
	//if err != nil {
	//	fmt.Println(err)
	//	return
	//}
	//
	//// Split the file list into chunks of maxFileSize
	//chunks := make([][]string, 0)
	//chunk := make([]string, 0)
	//var chunkSize int64
	//for _, file := range fileList {
	//	info, err := os.Stat(file)
	//	if err != nil {
	//		fmt.Println(err)
	//		return
	//	}
	//	if chunkSize+info.Size() > maxFileSize {
	//		chunks = append(chunks, chunk)
	//		chunk = make([]string, 0)
	//		chunkSize = 0
	//	}
	//	chunk = append(chunk, file)
	//	chunkSize += info.Size()
	//}
	//if len(chunk) > 0 {
	//	chunks = append(chunks, chunk)
	//}
	//
	//// Add chunks to the queue
	//for _, chunk := range chunks {
	//	queue <- chunk
	//}
	//
	//// Process the queue using a worker pool
	//for i := 0; i < 10; i++ {
	//	wg.Add(1)
	//	go func() {
	//		defer wg.Done()
	//		for chunk := range queue {
	//			tarName := fmt.Sprintf("chunk_%d.tar", time.Now().UnixNano())
	//			cmd := fmt.Sprintf("tar -cvf %s %s", tarName, filepath.Join(chunk...))
	//			if err := exec.Command("bash", "-c", cmd).Run(); err != nil {
	//				fmt.Println(err)
	//				return
	//			}
	//		}
	//	}()
	//}
	//
	//wg.Wait()

	//
	//// Initialize an empty slice to hold file names
	//var fileNames []string
	//
	//// Define a function to calculate the total size of a slice of files
	//totalSize := func(files []string) int64 {
	//	var total int64
	//	for _, file := range files {
	//		info, err := os.Stat(file)
	//		if err != nil {
	//			log.Fatal(err)
	//		}
	//		total += info.Size()
	//	}
	//	return total
	//}
	//
	//// Define a function to process a slice of files
	//processFiles := func(files []string) {
	//	// Do something with the files, such as adding them to a work queue
	//	fmt.Println("Processing files:", files)
	//}
	//
	//// Walk through the directory and add file names to the slice
	//err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
	//	if err != nil {
	//		return err
	//	}
	//	if !info.IsDir() {
	//		fileNames = append(fileNames, path)
	//	}
	//	return nil
	//})
	//if err != nil {
	//	log.Fatal(err)
	//}
	//
	//// Initialize an empty slice to hold a subset of file names
	//var subset []string
	//
	//// Loop through the file names and add them to the subset slice until the total size reaches 4MB
	//for _, fileName := range fileNames {
	//	subset = append(subset, fileName)
	//	if totalSize(subset) >= 4*1024*1024 {
	//		// If the total size of the subset is 4MB or greater, process the subset and reset the subset slice
	//		processFiles(subset)
	//		subset = nil
	//	}
	//}
	//
	//// If there are any remaining files in the subset slice, process them as well
	//if len(subset) > 0 {
	//	processFiles(subset)
	//}

	//rootDir := "/mnt/jfs" // 要遍历的根目录
	//tree(rootDir, 0)      // 调用tree函数遍历目录

	// Get the current directory or user-specified directory
	//dir := "/mnt/jfs"
	//if len(os.Args) > 1 {
	//	dir = os.Args[1]
	//}
	//// Start the recursive tree traversal
	//if err := tree(dir, "", true); err != nil {
	//	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	//	os.Exit(1)
	//}

	// Get the current directory or user-specified directory
	//dir := "/mnt/jfs"
	//if len(os.Args) > 1 {
	//	dir = os.Args[1]
	//}
	//// Start the recursive tree traversal
	//tree(dir, "", true, 0)
	//if err := tree(dir, "", true, 0); err != nil {
	//	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	//	os.Exit(1)
	//}

	// Get the current directory or user-specified directory
	//dir := "/mnt/jfs"
	//if len(flag.Args()) > 0 {
	//	dir = flag.Arg(0)
	//}
	//log.Println("tree")
	//// Start the recursive tree traversal
	//if err := tree(dir, "", true, 0); err != nil {
	//	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	//	os.Exit(1)
	//}
	//
	//printDirTree(dir, "")
	//
	//readdirtree(dir, 0)

	sort.Slice(fileList, func(i, j int) bool {
		dirI := filepath.Dir(fileList[i])
		dirJ := filepath.Dir(fileList[j])
		return dirI < dirJ
	})

	// 输出排序后的结果
	for _, filePath := range fileList {
		fmt.Println(filePath)
	}

	root := &Node1{}
	for _, filePath := range fileList {
		parts := strings.Split(filePath, "/")
		curr := root
		for _, part := range parts {
			child := getChild(curr, part)
			curr = child
		}
	}
	printTree(root, 0)

	rootNode := &utils.FileNode{}
	rootNode.MTree("/mnt/jfs")
	rootNode.ShowTree("")

}

type Node1 struct {
	Name     string
	Children []*Node1
}

func getChild(node *Node1, name string) *Node1 {
	for _, child := range node.Children {
		if child.Name == name {
			return child
		}
	}
	child := &Node1{Name: name, Children: []*Node1{}}
	node.Children = append(node.Children, child)
	return child
}
func printTree(node *Node1, level int) {

	fmt.Printf("%s%s\n", strings.Repeat("  ", level), node.Name)

	for _, child := range node.Children {
		printTree(child, level+1)
	}
}

// Defining a function to create a tree structure from a list of file paths
//func createTree(paths []string) map[string]interface{} {
//	// Creating an empty map to store the tree structure
//	tree := make(map[string]interface{})
//
//	// Looping through each path in the list
//	for _, path := range paths {
//		// Splitting the path into individual directories
//		parts := strings.Split(path, string(filepath.Separator))
//
//		// Creating a reference to the current level of the tree
//		current := tree
//
//		// Looping through each directory in the path
//		for _, part := range parts {
//			// If the directory does not exist in the current level of the tree, create it
//			if _, ok := current[part]; !ok {
//				current[part] = make(map[string]interface{})
//			}
//
//			// Updating the current level of the tree to the newly created directory
//			current = current[part].(map[string]interface{})
//		}
//	}
//
//	// Returning the completed tree structure
//	return tree
//}

//// Defining a function to print the tree structure
//func printTree(tree map[string]interface{}, indent string) {
//	// Looping through each directory in the current level of the tree
//	for key, value := range tree {
//		// Printing the directory name with appropriate indentation
//		fmt.Println(indent + key)
//
//		// If the value is a map, recursively calling the printTree function with increased indentation
//		if subTree, ok := value.(map[string]interface{}); ok {
//			printTree(subTree, indent+"  ")
//		}
//	}
//}

// Defining a function to display the file tree structure
func displayFileTree(files []string) {
	// Creating a map to store the file tree structure
	fileTree := make(map[string][]string)

	// Looping through each file path in the array
	for _, file := range files {
		// Splitting the file path into individual directories
		dirs := strings.Split(file, "/")

		// Looping through each directory in the file path
		for i := range dirs {
			// Creating a key for the current directory in the file tree map
			key := strings.Join(dirs[:i+1], "/")

			// Adding the file path to the list of files for the current directory
			fileTree[key] = append(fileTree[key], file)
		}
	}

	// Looping through each directory in the file tree map
	for dir, files := range fileTree {
		// Counting the number of subdirectories in the current directory
		subDirs := strings.Count(dir, "/")

		// Indenting the directory name based on the number of subdirectories
		indent := strings.Repeat("  ", subDirs)

		// Printing the directory name
		fmt.Printf("%s- %s\n", indent, dir)

		// Looping through each file in the current directory
		for _, file := range files {
			// Indenting the file name based on the number of subdirectories
			indent = strings.Repeat("  ", subDirs+1)

			// Printing the file name
			fmt.Printf("%s- %s\n", indent, file)
		}
	}
}

// Defining a function to print the directory tree
func printDirTree(path string, prefix string) {
	// Getting all the files and directories in the given path
	files, err := os.ReadDir(path)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Looping through all the files and directories
	for i, file := range files {
		if i == len(files)-1 {
			fmt.Printf("%s└── %s\n", prefix, file.Name())
			if file.IsDir() {
				printDirTree(filepath.Join(path, file.Name()), prefix+"    ")
			}
		} else {
			fmt.Printf("%s├── %s\n", prefix, file.Name())
			if file.IsDir() {
				printDirTree(filepath.Join(path, file.Name()), prefix+"│   ")
			}
		}
	}
}

var maxLevel int
var showDetail bool
var showHidden bool

const (
	pipe       = "│   "
	tee        = "├── "
	lasttee    = "└── "
	defaultDir = "."
)

func init() {
	flag.IntVar(&maxLevel, "L", -1, "max display level")
	flag.BoolVar(&showDetail, "l", false, "show detail info")
	flag.BoolVar(&showHidden, "a", false, "show hidden files")
	flag.Parse()
}

func tree(dirPath string, prefix string, isLast bool, level int) error {
	// If the directory does not exist, return an error
	d, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	defer d.Close()
	// Read the contents of the directory
	files, err := d.Readdir(-1)
	if err != nil {
		return err
	}
	// Print the prefix (if any) indicating whether this is the last file or not
	//fmt.Print(prefix)

	// Iterate through the files and directories
	for i, file := range files {
		// Skip hidden files and directories if not specified
		if !showHidden && strings.HasPrefix(file.Name(), ".") {
			continue
		}
		// Skip files and directories that lead to infinite recursion
		if file.IsDir() && (file.Name() == "." || file.Name() == "..") {
			continue
		}
		// If the directory has reached the maximum level of display
		if maxLevel >= 0 && level >= maxLevel {
			// ... but this is the last file or directory in the current level
			if i == len(files)-1 {
				return nil
			}
			// ... or this not the last file or directory in the current level
			continue
		}
		// Print the name of the file or directory
		if showDetail {
			if isLast {
				fmt.Printf("%s%s%s (%s)\n", prefix, lasttee, file.Name(), humanReadableSize(file.Size()))
			} else {
				fmt.Printf("%s%s%s (%s)\n", prefix, tee, file.Name(), humanReadableSize(file.Size()))
			}
		} else {
			if isLast {
				fmt.Printf("%s%s%s\n", prefix, lasttee, file.Name())
			} else {
				fmt.Printf("%s%s%s\n", prefix, tee, file.Name())
			}
		}
		// Recursively display subdirectories
		if file.IsDir() {
			if isLast {
				//fmt.Print(lasttee)
				prefix += "    "
			} else {
				//fmt.Print(tee)
				prefix += pipe
			}
			if err := tree(filepath.Join(dirPath, file.Name()), prefix, i == len(files)-1, level+1); err != nil {
				return err
			}
		}
	}
	return nil
}
func humanReadableSize(size int64) string {
	var result string
	switch {
	case size >= 1024*1024*1024:
		result = fmt.Sprintf("%.2fG", float64(size)/1024/1024/1024)
	case size >= 1024*1024:
		result = fmt.Sprintf("%.2fM", float64(size)/1024/1024)
	case size >= 1024:
		result = fmt.Sprintf("%.2fK", float64(size)/1024)
	default:
		result = fmt.Sprintf("%dB", size)
	}
	return result
}

//	func tree(dirPath string, prefix string, isLast bool, level int) error {
//		// If the directory does not exist, return an error
//		d, err := os.Open(dirPath)
//		if err != nil {
//			return err
//		}
//		defer d.Close()
//		// Read the contents of the directory
//		files, err := os.ReadDir(dirPath)
//		if err != nil {
//			return err
//		}
//		// Print the prefix (if any) indicating whether this is the last file or not
//		fmt.Print(prefix)
//		if isLast {
//			fmt.Print(elpse)
//			prefix += "    " // There are four spaces here
//		} else {
//			fmt.Print(ttee)
//			prefix += pipe // This is a pipe followed by three spaces
//		}
//		// Iterate through the files and directories
//		for i, file := range files {
//			if file.Name()[0] == '.' {
//				continue // Ignore hidden files and directories
//			}
//			if file.IsDir() {
//				// If this is a directory, print its name and recursively call tree
//				//fmt.Printf("%s\n", file.Name())
//				fmt.Printf("%s%s %s\n", getIndent(level), elpse, file.Name())
//				if err := tree(filepath.Join(dirPath, file.Name()), prefix, i == len(files)-1, level+1); err != nil {
//					return err
//				}
//			} else {
//				// If this is a file, just print its name
//				//info, _ := file.Info()
//				//fmt.Printf("%s (%s)\n", file.Name(), humanReadableSize(info.Size()))
//				fmt.Printf("%s%s %s\n", getIndent(level), ttee, file.Name())
//			}
//		}
//		return nil
//	}
//func humanReadableSize(size int64) string {
//	var result string
//	switch {
//	case size >= 1024*1024*1024:
//		result = fmt.Sprintf("%.2fG", float64(size)/1024/1024/1024)
//	case size >= 1024*1024:
//		result = fmt.Sprintf("%.2fM", float64(size)/1024/1024)
//	case size >= 1024:
//		result = fmt.Sprintf("%.2fK", float64(size)/1024)
//	default:
//		result = fmt.Sprintf("%dB", size)
//	}
//	return result
//}

//func tree(dir string, level int) {
//	files, err := os.ReadDir(dir) // 获取目录下所有文件和目录的信息
//	if err != nil {
//		fmt.Println(err)
//		return
//	}
//	for _, file := range files {
//		fileName := file.Name()
//		if file.IsDir() { // 如果是目录，递归遍历子目录
//			fmt.Printf("%s|-- %s\n", getIndent(level), fileName)
//			tree(filepath.Join(dir, fileName), level+1)
//		} else { // 如果是文件，直接输出文件名
//			fmt.Printf("%s|-- %s\n", getIndent(level), fileName)
//		}
//	}
//}
//func getIndent(level int) string {
//	indent := ""
//	for i := 0; i < level; i++ {
//		indent += "    "
//	}
//	return indent
//}
//
//const (
//	pipe       = "│   "
//	ttee       = "├── "
//	elpse      = "└── "
//	defaultDir = "."
//)

//func tree(dir string, prefix string, isLast bool, level int) {
//	files, err := os.ReadDir(dir) // 获取目录下所有文件和目录的信息
//	if err != nil {
//		fmt.Println(err)
//		return
//	}
//
//	//var prefixes []string
//	if isLast { // 如果是最后一个节点，把前缀换成空格
//		//prefixes = append(prefixes, prefix+"└── ")
//		prefix += "    "
//	} else { // 不是最后一个节点，把前缀换成竖线
//		//prefixes = append(prefixes, prefix+"├── ")
//		prefix += "│   "
//	}
//
//	for i, file := range files {
//		fileName := file.Name()
//		if file.IsDir() { // 如果是目录，递归遍历子目录
//			var nodePrefix string
//			if i == len(files)-1 { // 如果是最后一个节点，说明子目录也是最后一个节点
//				nodePrefix = elpse
//			} else { // 如果不是最后一个节点，说明子目录后面还有其他节点
//				nodePrefix = ttee
//			}
//			fmt.Printf("%s%s %s\n", getIndent(level), nodePrefix, fileName)
//			tree(filepath.Join(dir, fileName), prefix, i == len(files)-1, level+1)
//		} else { // 如果是文件，直接输出文件名
//			var nodePrefix string
//			if i == len(files)-1 { // 如果是最后一个节点，说明文件也是最后一个节点
//				nodePrefix = elpse
//			} else { // 如果不是最后一个节点，说明文件后面还有其他节点
//				nodePrefix = ttee
//			}
//			//fmt.Printf("%s%s\n", nodePrefix, fileName)
//			fmt.Printf("%s%s %s\n", getIndent(level), nodePrefix, file.Name())
//		}
//	}
//}

//const (
//	pipe       = "│   "
//	ttee       = "├── "
//	elpse      = "└── "
//	defaultDir = "."
//)
//
//func tree(dirPath string, prefix string, isLast bool) error {
//	// If the directory does not exist, return an error
//	d, err := os.Open(dirPath)
//	if err != nil {
//		return err
//	}
//	defer d.Close()
//	// Read the contents of the directory
//	files, err := os.ReadDir(dirPath)
//	if err != nil {
//		return err
//	}
//	// Print the prefix (if any) indicating whether this is the last file or not
//	fmt.Print(prefix)
//	if isLast {
//		fmt.Print(elpse)
//		prefix += "    " // There are four spaces here
//	} else {
//		fmt.Print(ttee)
//		prefix += pipe // This is a pipe followed by three spaces
//	}
//	// Iterate through the files and directories
//	for i, file := range files {
//		if file.Name()[0] == '.' {
//			continue // Ignore hidden files and directories
//		}
//		if file.IsDir() {
//			// If this is a directory, print its name and recursively call tree
//			fmt.Printf("%s\n", file.Name())
//			if err := tree(filepath.Join(dirPath, file.Name()), prefix, i == len(files)-1); err != nil {
//				return err
//			}
//		} else {
//			// If this is a file, just print its name
//			fmt.Printf("%s\n", file.Name())
//		}
//	}
//	return nil
//}

//	func (fs *MyFUSEFileSystem) readdir(path string, fill func(name string, stat *fuse.Stat_t, off int64) bool) error {
//		// Open the directory at the given path
//		dir, err := os.Open(path)
//		if err != nil {
//			return err
//		}
//		defer dir.Close()
//		fileInfos, err := dir.Readdir(-1)
//		if err != nil {
//			return err
//		}
//		for _, fileInfo := range fileInfos {
//			stat := new(fuse.Stat_t)
//			if fileInfo.IsDir() {
//				stat.Mode = fuse.S_IFDIR | 0755
//				name := fileInfo.Name() + "/"
//				fill(name, stat, 0)
//				if fs.tree {
//					fs.readdirtree(filepath.Join(path, fileInfo.Name()), fill, 1)
//				}
//			} else {
//				stat.Mode = fuse.S_IFREG | 0644
//				name := fileInfo.Name()
//				fill(name, stat, 0)
//			}
//		}
//		return nil
//	}
func readdirtree(path string, indent int) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	fileInfos, err := dir.Readdir(-1)
	if err != nil {
		return err
	}
	for _, fileInfo := range fileInfos {
		if fileInfo.IsDir() {
			name := fileInfo.Name() + "/"
			fmt.Println(strings.Repeat("    ", indent) + name)
			readdirtree(filepath.Join(path, fileInfo.Name()), indent+1)
		} else {
			name := fileInfo.Name()
			fmt.Println(strings.Repeat("    ", indent) + name)
		}
	}
	return nil
}

const (
	maxFileSize = 4 * 1024 * 1024 // 4MB
	numWorkers  = 4               // number of workers in the thread pool
)

func extractFilePaths(dirPath string, filePaths chan<- string) {
	// walk the directory tree
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error walking path %s: %s", path, err)
			return nil
		}

		// if the path is a file, send it to the channel
		if !info.IsDir() {
			filePaths <- path
		}

		return nil
	})

	if err != nil {
		log.Printf("Error walking directory %s: %s", dirPath, err)
	}

	// close the file paths channel
	close(filePaths)
}

func worker(filePathArrays <-chan []string, wg *sync.WaitGroup) {
	// loop over the file path arrays received from the channel
	for filePathArray := range filePathArrays {
		// create a tar file
		tarFile, err := os.CreateTemp("/mnt/jfs2", "tar")
		if err != nil {
			log.Printf("Error creating tar file: %s", err)
			continue
		}

		// create a new tar writer
		tarWriter := tar.NewWriter(tarFile)

		// loop over the file paths in the array
		for _, filePath := range filePathArray {
			// open the file
			file, err := os.Open(filePath)
			if err != nil {
				log.Printf("Error opening file %s: %s", filePath, err)
				continue
			}

			// get the file info
			fileInfo, err := file.Stat()
			if err != nil {
				log.Printf("Error getting file info for %s: %s", filePath, err)
				continue
			}

			// create a new header for the file
			header := &tar.Header{
				Name:    fileInfo.Name(),
				Size:    fileInfo.Size(),
				Mode:    int64(fileInfo.Mode()),
				ModTime: fileInfo.ModTime(),
			}

			// write the header to the tar file
			err = tarWriter.WriteHeader(header)
			if err != nil {
				log.Printf("Error writing header for %s: %s", filePath, err)
				continue
			}

			// copy the file contents to the tar file
			_, err = io.Copy(tarWriter, file)
			if err != nil {
				log.Printf("Error copying file %s to tar file: %s", filePath, err)
				continue
			}

			// close the file
			err = file.Close()
			if err != nil {
				log.Printf("Error closing file %s: %s", filePath, err)
				continue
			}
		}

		// close the tar writer
		err = tarWriter.Close()
		if err != nil {
			log.Printf("Error closing tar writer: %s", err)
			continue
		}

		// close the tar file
		err = tarFile.Close()
		if err != nil {
			log.Printf("Error closing tar file: %s", err)
			continue
		}

		// remove the file path array from the channel
		<-filePathArrays
	}

	// signal that the worker has finished
	wg.Done()
}

// getAllFilePaths returns a list of all file paths in the directory
func getAllFilePaths(dirPath string) ([]string, error) {
	var filePaths []string
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			filePaths = append(filePaths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return filePaths, nil
}

// getSliceSize returns the total size of all files in the slice
func getSliceSize(filePaths []string) int64 {
	var size int64
	for _, filePath := range filePaths {
		fileInfo, err := os.Stat(filePath)
		if err != nil {
			log.Printf("error getting file info for %s: %v", filePath, err)
			continue
		}
		size += fileInfo.Size()
	}
	return size
}

// createTar creates a tar file for the given file paths
func createTar(filePaths ...string) error {
	// create tar file
	tarFilePath := fmt.Sprintf("%s.tar", time.Now().Format("2006-01-02_15-04-05"))
	tarFile, err := os.Create(tarFilePath)
	if err != nil {
		return err
	}
	defer tarFile.Close()

	// create tar writer
	tarWriter := tar.NewWriter(tarFile)
	defer tarWriter.Close()

	// add files to tar
	for _, filePath := range filePaths {
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer file.Close()

		fileInfo, err := file.Stat()
		if err != nil {
			return err
		}

		header := &tar.Header{
			Name:    fileInfo.Name(),
			Size:    fileInfo.Size(),
			Mode:    int64(fileInfo.Mode()),
			ModTime: fileInfo.ModTime(),
		}

		err = tarWriter.WriteHeader(header)
		if err != nil {
			return err
		}

		_, err = io.Copy(tarWriter, file)
		if err != nil {
			return err
		}
	}

	return nil
}

func globalFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:    "verbose",
			Aliases: []string{"debug", "v"},
			Usage:   "enable debug log",
		},
		&cli.BoolFlag{
			Name:    "quiet",
			Aliases: []string{"q"},
			Usage:   "show warning and errors only",
		},
		&cli.BoolFlag{
			Name:  "trace",
			Usage: "enable trace log",
		},
		&cli.BoolFlag{
			Name:  "no-agent",
			Usage: "disable pprof (:6060) and gops (:6070) agent",
		},
		&cli.StringFlag{
			Name:  "pyroscope",
			Usage: "pyroscope address",
		},
		&cli.BoolFlag{
			Name:  "no-color",
			Usage: "disable colors",
		},
	}
}

var logger = utils.GetLogger("wjy")

// Check number of positional arguments, set logger level and setup agent if needed
func setup(c *cli.Context, n int) {
	if c.NArg() < n {
		fmt.Printf("ERROR: This command requires at least %d arguments\n", n)
		fmt.Printf("USAGE:\n   juicefs %s [command options] %s\n", c.Command.Name, c.Command.ArgsUsage)
		os.Exit(1)
	}

	if c.Bool("trace") {
		utils.SetLogLevel(logrus.TraceLevel)
	} else if c.Bool("verbose") {
		utils.SetLogLevel(logrus.DebugLevel)
	} else if c.Bool("quiet") {
		utils.SetLogLevel(logrus.WarnLevel)
	} else {
		utils.SetLogLevel(logrus.InfoLevel)
	}
	if c.Bool("no-color") {
		utils.DisableLogColor()
	}

	if !c.Bool("no-agent") {
		go func() {
			for port := 6060; port < 6100; port++ {
				_ = http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", port), nil)
			}
		}()
		go func() {
			for port := 6070; port < 6100; port++ {
				_ = agent.Listen(agent.Options{Addr: fmt.Sprintf("127.0.0.1:%d", port)})
			}
		}()
	}

	if c.IsSet("pyroscope") {
		tags := make(map[string]string)
		appName := fmt.Sprintf("juicefs.%s", c.Command.Name)
		if c.Command.Name == "mount" {
			tags["mountpoint"] = c.Args().Get(1)
		}
		if hostname, err := os.Hostname(); err == nil {
			tags["hostname"] = hostname
		}
		tags["pid"] = strconv.Itoa(os.Getpid())
		tags["version"] = "v1"

		if _, err := pyroscope.Start(pyroscope.Config{
			ApplicationName: appName,
			ServerAddress:   c.String("pyroscope"),
			Logger:          logger,
			Tags:            tags,
			AuthToken:       os.Getenv("PYROSCOPE_AUTH_TOKEN"),
			ProfileTypes:    pyroscope.DefaultProfileTypes,
		}); err != nil {
			logger.Errorf("start pyroscope agent: %v", err)
		}
	}
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
