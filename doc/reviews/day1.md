### day1

- `go mod init github.com/travisjeffery/proglog` 初始化 mod 并且带路径的。

- protobuf 

  ```shell
  protoc api/v1/*.proto \
  				--go_out=. \
  				--go_opt=paths=source_relative \
  				--proto_path=.
  ## sudo apt install protoc-gen-go  | brew install protoc-gen-go
  ```

- Write a Log Package.  核心的分布式基础数据结构。

  > Logs—which are sometimes also called write-ahead logs, transaction logs, or commit logs—are at the heart of storage engines, message queues, version control, and replication and consensus algorithms.

  - 特性：结构简单，顺序性，读写性能优，可重放恢复等。

  - 本次构建的 log 系统包含一下组件：

    - `Record` - 存储在 log 中的数据
    - `Store` - 用来存储 log 的文件
    - `Index` - 用来存储 Record 在 Store 中的索引信息
    - `Segment` - 将 Store 和 Index 绑定的抽象层(毕竟磁盘空间有限，需要滚动存储)
    - `Log` - 将所有 Segment 绑定的抽象概念

  - **store.go**

    ```go
    package log
    
    import (
    	"bufio"
    	"encoding/binary"
    	"os"
    	"sync"
    )
    
    var (
    	enc = binary.BigEndian
    )
    
    const (
    	// 专门用来存储 record 的 byte 数，用于后续读取
    	lenWidth = 8
    )
    
    type store struct {
    	*os.File
    	mu sync.Mutex
    	buf *bufio.Writer
    	size uint64
    }
    
    func newStore(f *os.File) (*store, error) {
    	// Stat returns a [FileInfo] describing the named file.
    	fi, err := os.Stat(f.Name())
    	if err != nil {
    		return nil, err
    	}
    	size := uint64(fi.Size())
    	return &store{
    		File: f,
    		size: size,
    		buf: bufio.NewWriter(f),
    	}, nil
    }
    
    // p: data to write.
    // n: number of bytes written.
    // pos: start position of the record,the segment will use this postion 
    //      when it creates an associated index entry for this record.
    func (s *store) Append(p []byte) (n uint64, pos uint64, err error) {
    	s.mu.Lock()
    	defer s.mu.Unlock()
    	pos = s.size
        // 先存 p 的字节数据长度(先填充 lenWidth)
    	if err := binary.Write(s.buf, enc, uint64(len(p))); err != nil {
    		return 0, 0, err
    	}
    	// 将 record 数据刷到 buffer 中而不直接刷盘以减少系统调用提高性能
    	w, err := s.buf.Write(p)
    	if err != nil {
    		return 0, 0, err
    	}
    	w += lenWidth
    	s.size += uint64(w)
    	return uint64(w), pos, nil
    }
    
    func (s *store) Read(pos uint64) ([]byte, error) {
    	s.mu.Lock()
    	defer s.mu.Unlock()
    	// 首先刷盘下，避免要读的 record 还没刷盘
    	if err := s.buf.Flush(); err != nil {
    		return nil, err
    	}
    	size := make([]byte, lenWidth)
    	if _, err := s.File.ReadAt(size, int64(pos)); err != nil {
    		return nil, err
    	}
    	b := make([]byte, enc.Uint64(size))
    	if _, err := s.File.ReadAt(b, int64(pos+lenWidth)); err != nil {
    		return nil, err
    	}
    	return b, nil
    }
    
    func (s *store) ReadAt(p []byte, off int64) (int, error) {
    	s.mu.Lock()
    	defer s.mu.Unlock()
    	if err := s.buf.Flush(); err != nil {
    		return 0, err
    	}
    	return s.File.ReadAt(p, off)
    }
    
    func (s *store) Close() error {
    	s.mu.Lock()
    	defer s.mu.Unlock()
    	err := s.buf.Flush()
    	if err != nil {
    		return err
    	}
    	return s.File.Close()
    }
    ```

    

  - **index.go**

    ```go
    package log
    
    import (
    	"io"
    	"os"
    
    	"github.com/tysonmote/gommap"
    )
    
    var (
        // record 在 store 文件中的 offset, 存储 uint32 -> 4 bytes
    	offWidth uint64 = 4
        // record 在 store 文件中的 position, 存储 uint64 -> 8 bytes
    	posWidth uint64 = 8
    	entWidth = offWidth + posWidth
    )
    
    type index struct {
    	file *os.File
    	mmap gommap.MMap
        // 当前 index 文件大小，以及下个 index entry 的写入位置
    	size uint64
    }
    
    // 需要注意的一点是 file 与 mmap file 之间可能有差异，需要 Truncate 文件实际数据
    func newIndex(f *os.File, c Config) (*index, error) {
    	idx := &index{
    		file: f,
    	}
    	fi, err := os.Stat(f.Name())
    	if err != nil {
    		return nil, err
    	}
    	idx.size = uint64(fi.Size())
    	// Truncate changes the size of the file.
    	// It does not change the I/O offset.
    	if err = os.Truncate(
    			f.Name(), int64(c.Segment.MaxIndexBytes),
    	); err != nil {
    		return nil, err
    	}
    	if idx.mmap, err = gommap.Map(
    		idx.file.Fd(),
    		gommap.PROT_READ|gommap.PROT_WRITE,
    		gommap.MAP_SHARED,
    	); err != nil {
    		return nil, err
    	}
    	return idx, nil
    }
    
    // Read takes in an offset and returns the associated record's position in the store.
    // The given offset is relative to the segment's base offset;
    // 0 is the index's first entry, 1 is the second entry,and so on...
    func (i *index) Read(in int64) (out uint32, pos uint64, err error) {
    	if i.size == 0 {
    		return 0, 0, io.EOF
    	}
    	if in == -1 {
    		out = uint32((i.size) / entWidth - 1)
    	} else {
    		out = uint32(in)
    	}
    	pos = uint64(out) * entWidth
    	if  i.size < pos+entWidth {
    		return 0, 0, io.EOF
    	}
    	out = enc.Uint32(i.mmap[pos : pos+offWidth])
    	pos = enc.Uint64(i.mmap[pos+offWidth : pos+entWidth])
    	return out, pos, nil
     }
    
     func (i *index) Write(off uint32, pos uint64) error {
    	if uint64(len(i.mmap)) < i.size + entWidth {
    		return io.EOF
    	}
    	enc.PutUint32(i.mmap[i.size:i.size+offWidth], off)
    	enc.PutUint64(i.mmap[i.size+offWidth:i.size+entWidth], pos)
    	i.size += uint64(entWidth)
    	return nil
     }
    
     func (i *index) Name() string {
    	return i.file.Name()
     }
    
    func (i *index) Close() error {
        // mmap -> file
    	if err := i.mmap.Sync(gommap.MS_SYNC); err != nil {
    		return err
    	}
        // file -> disk
    	if err := i.file.Sync(); err != nil {
    		return err
    	}
    	if err := i.file.Truncate(int64(i.size)); err != nil {
    		return err
    	}
    	return i.file.Close()
    }
    ```

    - **关于需要 Truncate 文件的描述**

      ```
      	Now that we’ve seen the code for both opening and closing an index, we can
      discuss what this growing and truncating the file business is all about.
      	When we start our service, the service needs to know the offset to set on the next record appended to the log. The service learns the next record’s offset by looking at the last entry of the index, a simple process of reading the last 12 bytes of the file. However, we mess up this process when we grow the files so we can memory-map them. (The reason we resize them now is that, once they’re memory-mapped, we can’t resize them, so it’s now or never.) We grow the files by appending empty space at the end of them, so the last entry is no longer at the end of the file—instead, there’s some unknown amount of space between this entry and the file’s end. This space prevents the service from restarting properly. That’s why we shut down the service by truncating the
      index files to remove the empty space and put the last entry at the end of the
      file once again. This graceful shutdown returns the service to a state where
      it can restart properly and efficiently.
      ```

    - **Handling Ungraceful Shutdowns**

      > A graceful shutdown occurs when a service finishes its ongoing
      > tasks, performs its processes to ensure there’s no data loss, and
      > prepares for a restart. If your service crashes or its hardware fails,
      > you’ll experience an ungraceful shutdown. An example of an
      > ungraceful shutdown for the service we’re building would be if it
      > lost power before it finished truncating its index files. You handle
      > ungraceful shutdowns by performing a sanity check when your
      > service restarts to find corrupted data. If you have corrupted data,
      > you can rebuild the data or replicate the data from an uncorrupted
      > source. The log we’re building doesn’t handle ungraceful shut-
      > downs because I wanted to keep the code simple.