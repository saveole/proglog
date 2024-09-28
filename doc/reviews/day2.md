### day2

#### Segment

- segment 将 store 和 index 组合在一起。写入 record 时先写入 store，再将 index 数据写入 index file;读取 record 则相反，先通过读取 index 中 record 的位置信息，再去 store 中读取数据。

- 另外还需要标记 record 存储的初始 offset 和 nextOffset, 以便于查找。

- 主要结构：

  ```go
  type segment struct {
  	store                  *store
  	index                  *index
      // 成功 append 一个 record 后， nextOffset++
  	baseOffset, nextOffset uint64
  	config                 Config
  }
  ```

- 主要方法：

  - 初始化: 指定存储路径，基础 offset，config 配置

    ```go
    func newSegment(dir string, baseOffset uint64, c Config) (*segment, error) {
    	s := &segment{
    		baseOffset: baseOffset,
    		config:     c,
    	}
    	var err error
    	storeFile, err := os.OpenFile(
    		path.Join(dir, fmt.Sprintf("%d%s", baseOffset, ".store")),
    		os.O_RDWR|os.O_CREATE|os.O_APPEND,
    		0644,
    	)
    	if err != nil {
    		return nil, err
    	}
    	if s.store, err = newStore(storeFile); err != nil {
    		return nil, err
    	}
    	indexFile, err := os.OpenFile(
    		path.Join(dir, fmt.Sprintf("%d%s", baseOffset, ".index")),
    		os.O_RDWR|os.O_CREATE,
    		0644,
    	)
    	if err != nil {
    		return nil, err
    	}
    	if s.index, err = newIndex(indexFile, c); err != nil {
    		return nil, err
    	}
    	if off, _, err := s.index.Read(-1); err != nil {
    		s.nextOffset = baseOffset
    	} else {
    		s.nextOffset = baseOffset + uint64(off) + 1
    	}
    	return s, nil
    }
    ```

  - Append 数据写入

    ```go
    func (s *segment) Append(record *api.Record) (offset uint64, err error) {
    	cur := s.nextOffset
    	record.Offset = cur
    	p, err := proto.Marshal(record)
    	if err != nil {
    		return 0, err
    	}
    	_, pos, err := s.store.Append(p)
    	if err != nil {
    		return 0, err
    	}
    	if err = s.index.Write(
    		// index offsets are relative to base offset
    		uint32(s.nextOffset-s.baseOffset),
    		pos,
    	); err != nil {
    		return 0, err
    	}
    	s.nextOffset++
    	return cur, nil
    }
    ```

  - Read 数据读取

    ```go
    func (s *segment) Read(offset uint64) (*api.Record, error) {
    	_, pos, err := s.index.Read(int64(offset - s.baseOffset))
    	if err != nil {
    		return nil, err
    	}
    	p, err := s.store.Read(pos)
    	if err != nil {
    		return nil, err
    	}
    	record := &api.Record{}
    	err = proto.Unmarshal(p, record)
    	return record, err
    }
    ```

  - IsMaxed 是否到达设置的最大存储阈值

    ```go
    func (s *segment) IsMaxed() bool {
    	return s.store.size >= s.config.Segment.MaxStoreBytes ||
    		s.index.size >= s.config.Segment.MaxIndexBytes
    }
    ```

    


#### Log

- log 是用于管理 segment 列表的组件。

- segment 是抽象概念，实际没有其相应的文件存储。

- 主要结构：

  ```go
  type Log struct {
      // 读写锁用于并发控制
  	mu            sync.RWMutex
  	Dir           string
  	Config        Config
      // 当前活跃 segment
  	activeSegment *segment
  	segments      []*segment
  }
  ```

- 主要方法：

  - 初始化

    ```go
    func NewLog(dir string, c Config) (*Log, error) {
    	if c.Segment.MaxStoreBytes == 0 {
    		c.Segment.MaxStoreBytes = 1024
    	}
    	if c.Segment.MaxIndexBytes == 0 {
    		c.Segment.MaxIndexBytes = 1024
    	}
    	l := &Log{
    		Dir:    dir,
    		Config: c,
    	}
    	return l, l.setup()
    }
    
    func (l *Log) setup() error {
    	files, err := os.ReadDir(l.Dir)
    	if err != nil {
    		return err
    	}
        // 记录每个存在 segment 的 baseOffset
    	var baseOffsets []uint64
    	for _, file := range files {
    		offStr := strings.TrimSuffix(
    			file.Name(),
    			path.Ext(file.Name()),
    		)
            // segment baseOffset 用来命名了文件，所以这里通过截取文件名获取
    		off, _ := strconv.ParseUint(offStr, 10, 0)
    		baseOffsets = append(baseOffsets, off)
    	}
        // 正序来拍个序
    	sort.Slice(baseOffsets, func(i, j int) bool {
    		return baseOffsets[i] < baseOffsets[j]
    	})
    	for i := 0; i < len(baseOffsets); i++ {
    		if err = l.newSegment(baseOffsets[i]); err != nil {
    			return err
    		}
    		// baseOffset contains dup for index and store so we skip the dup
    		i++
    	}
        // 没有新建
    	if l.segments == nil {
    		if err = l.newSegment(
    			l.Config.Segment.InitialOffset,
    		); err != nil {
    			return err
    		}
    	}
    	return nil
    }
    
    func (l *Log) newSegment(off uint64) error {
    	s, err := newSegment(l.Dir, off, l.Config)
    	if err != nil {
    		return err
    	}
    	l.segments = append(l.segments, s)
        // 每个新建的 segment 都变为 activeSegment
    	l.activeSegment = s
    	return nil
    }
    ```

  - Append 写入

    ```go
    func (l *Log) Append(record *api.Record) (uint64, error) {
    	l.mu.Lock()
    	defer l.mu.Unlock()
    	off, err := l.activeSegment.Append(record)
    	if err != nil {
    		return 0, err
    	}
        // 写入后检查是否到达存储阈值，到了就新建 segment
    	if l.activeSegment.IsMaxed() {
    		err = l.newSegment(off + 1)
    	}
    	return off, err
    }
    ```

  - Read 读取

    ```go
    func (l *Log) Read(off uint64) (*api.Record, error) {
    	l.mu.RLock()
    	defer l.mu.RUnlock()
    	var s *segment
    
    	// segments are sorted by base offset from oldest to newest
        // 首先定位在哪个 segment 中
    	for _, segment := range l.segments {
    		if segment.baseOffset <= off && off < segment.nextOffset {
    			s = segment
    			break
    		}
    	}
    	if s == nil || s.nextOffset <= off {
    		return nil, fmt.Errorf("offset out of range: %d", off)
    	}
    	return s.Read(off)
    }
    ```

  - Truncate 截取

    ```go
    // Truncate(lowest uint64) removes all segments whose highest offset is lower than
    // lowest. 截取 off 水位以上的数据。
    func (l *Log) Truncate(lowest uint64) error {
    	l.mu.Lock()
    	defer l.mu.Unlock()
    	var segments []*segment
    	for _, s := range l.segments {
    		if s.nextOffset <= lowest+1 {
    			if err := s.Remove(); err != nil {
    				return err
    			}
    			continue
    		}
    		segments = append(segments, s)
    	}
    	l.segments = segments
    	return nil
    }
    ```

  - offset 低水位

    ```go
    func (l *Log) LowestOffset() (uint64, error) {
    	l.mu.RLock()
    	defer l.mu.RUnlock()
    	return l.segments[0].baseOffset, nil
    }
    ```

  - offset 高水位

    ```go
    func (l *Log) HighestOffset() (uint64, error) {
    	l.mu.RLock()
    	defer l.mu.RUnlock()
    	off := l.segments[len(l.segments)-1].nextOffset
    	if off == 0 {
    		return 0, nil
    	}
    	return off - 1, nil
    }
    ```

  - reader 用于读取全部 segment 内存

    ```go
    func (l *Log) Reader() io.Reader {
    	l.mu.RLock()
    	defer l.mu.RUnlock()
    	readers := make([]io.Reader, len(l.segments))
    	for i, segment := range l.segments {
    		readers[i] = &originReader{segment.store, 0}
    	}
    	return io.MultiReader(readers...)
    }
    
    type originReader struct {
    	*store
    	off int64
    }
    
    func (o *originReader) Read(p []byte) (int, error) {
    	n, err := o.ReadAt(p, o.off)
    	o.off += int64(n)
    	return n, err
    }
    ```

    