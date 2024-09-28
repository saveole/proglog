### day2

#### Segment

- segment 将 store 和 index 组合在一起。写入 record 时先写入 store，再将 index 数据写入 index file;读取 record 则相反，先通过读取 index 中 record 的位置信息，再去 store 中读取数据。

#### Log