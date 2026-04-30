package ebpf

// MaxDataSize must match MAX_DATA_SIZE in bpf/capture.c.
const MaxDataSize = 2048

const (
	DirEgress  uint8 = 0
	DirIngress uint8 = 1
)

// DataEvent is the binary layout of struct data_event read from the BPF ring
// buffer. Field order and padding must match the C struct exactly.
type DataEvent struct {
	Timestamp uint64
	PID       uint32
	FD        uint32
	DataLen   uint32
	DstIP     uint32
	DstPort   uint16
	Direction uint8
	_         uint8
	Data      [MaxDataSize]byte
}
