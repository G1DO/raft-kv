package log

import (
	"fmt"
	"os"
    "io"
    "encoding/binary"
)

// Log handles persistent storage of entries to disk.
// This is the source of truth — survives crashes.
type Log struct {
    path    string
    file    *os.File
    offsets []int64
    // its array for number the byte that refer to 
    //Entry 0: "PUT ahmed in table users" (23 bytes of data)
    //so array [0,23,3123,12312,12312]
    // array[0] its refer to 23 

}
func NewLog(path string) (*Log, error){

    file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
    
	if (err != nil){
        return nil, fmt.Errorf("failed to open log file: %w", err)
        //fmt.Errorf("something happen  with file createing ")
       // return 
    }
//case if log already exist what we will do in those offsets 
//later we will fix that issue

    return &Log{
    path:    path,
    file:    file,
    offsets: []int64{},
    }, nil
	
}
func (l *Log) Append(entry []byte) (int, error) {
    // 1. Get current position in file
    offset, err := l.file.Seek(0, io.SeekCurrent)
    if (err != nil){
        return 0,fmt.Errorf("failed to get index log file: %w", err)
    }

    // 2. Write length (how many bytes in entry)
    err = binary.Write(l.file, binary.LittleEndian, uint32(len(entry)))
    if (err!=nil){
         return 0,fmt.Errorf("failed to get the length of entry log file: %w", err)
    }
    // 3. Write the entry

    _, err = l.file.Write(entry)
    if (err!=nil){
         return 0,fmt.Errorf("failed to write in entry log file: %w", err)
    }
    // 4. Sync
    //force everything to disk rn not waiting for os its force and saying okay i am waiting here util u update it now
    l.file.Sync()

    // 5. Record offset
    l.offsets = append(l.offsets, offset)

    // 6. Return index
    return len(l.offsets) - 1 ,nil
}

func (l *Log)Get(index int)([]byte,error){
    //1. check index tb lw hoa 100 hn3ml eh
    if index < 0 || index >= len(l.offsets) {
    return nil, fmt.Errorf("index %d out of range", index)
}

    //2 where is entry start after the offest so we need to move int size 4 byte
    _, err := l.file.Seek(l.offsets[index], io.SeekStart)
    if (err != nil){
            return nil, fmt.Errorf("index %d cant get pointed on", index)

    }
    var length uint32
    //it read 4 bytes cuz this file know what var u give him if u give him 2 bytes if u give 4 like we did it will give u 4 bytes read
    err = binary.Read(l.file, binary.LittleEndian, &length)
if err != nil {
    return nil, fmt.Errorf("failed to read length: %w", err)
}
    entry := make([]byte ,length)
    _, err = l.file.Read(entry)
    if err != nil {
    return nil, fmt.Errorf("failed to read entry: %w", err)
}
    return entry ,nil
}

func (l *Log)LastIndex()(int){
    return len(l.offsets) - 1
}


func (l *Log)Replay()([][]byte,error ){
   _, err := l.file.Seek(0, io.SeekStart)
if err != nil {
    return nil, err
}

//resest
    l.offsets = []int64{}
    var entries [][]byte
    for {
        //read index 
        offset, err := l.file.Seek(0, io.SeekCurrent)
if err != nil {
        return nil, err
    }
        //move 4 bytes and get those value from index 
        var length uint32
        err = binary.Read(l.file,binary.LittleEndian,&length)

        if err == io.EOF {
    break  // no more entries, exit the loop
} 
if err != nil {
    return nil, err  // some other error
}

        // store it in byte
entry :=make([]byte,length)
_, err = l.file.Read(entry)
if err != nil {
    return nil, err
}
l.offsets = append(l.offsets, offset)
entries = append(entries, entry)



    }

    //return it here
return entries, nil


}
//
// You will implement:
//   - Append(entry) → index doneeeeeeeeeeee
//   - Get(index) → entry  doneee
//   - LastIndex() → index
//   - Replay() → iterate all entries
