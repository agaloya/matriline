// pcapsampler - permanent, size-bounded packet log for the Matriline lab router.
//
// It opens one AF_PACKET raw socket bound to ALL interfaces of the network
// namespace it runs in (the router's "core" internet namespace), keeps only
// packets RECEIVED by an interface (so a packet routed between two homes is
// stored once, not once per hop), keeps only IPv4 packets whose source AND
// destination belong to the lab's public address ranges (inter-node traffic;
// downloads from the real internet are ignored unless -all is given) and
// writes them to classic pcap files (nanosecond variant) rotated every
// -rotate bytes.
//
// Sampling rule ("stage" = 1 + floor(total/stage-bytes)), with total = bytes
// already stored by this program (monotonic, survives deletion of old files):
//
//	total <  1 GB  -> keep every packet          (1 of 1)
//	1 GB .. 2 GB   -> keep 1 of 2 eligible packets
//	2 GB .. 3 GB   -> keep 1 of 3 ...
//	total >= 6 GB  -> stop capturing for good
//
// Counters live in a JSON state file written atomically (write tmp + fsync +
// rename) once per second and on rotation/exit. After a power cut the last
// pcap file is repaired (truncated to its last complete record) and the
// totals are recomputed from the state + the real size of that file, so the
// rule continues exactly where it stopped.
//
// References (block level):
//   - packet(7) Linux man page: AF_PACKET, sockaddr_ll, PACKET_OUTGOING,
//     MSG_TRUNC semantics for packet sockets.
//   - socket(7)/SO_TIMESTAMPNS for kernel receive timestamps.
//   - pcap file format: https://wiki.wireshark.org/Development/LibpcapFileFormat
//     and draft-ietf-opsawg-pcap (magic 0xa1b23c4d = nanosecond timestamps).
//
// Only the Go standard library is used.
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	pcapMagicNS     = 0xa1b23c4d // classic pcap, nanosecond timestamps
	linktypeEther   = 1
	globalHdrLen    = 24
	recordHdrLen    = 16
	ethPAll         = 0x0003
	ethPIPv4        = 0x0800
	ethPVLAN        = 0x8100
	filePrefix      = "capture-"
	fileSuffix      = ".pcap"
	stateSavePeriod = time.Second
)

// State is persisted as JSON. All byte counts include pcap headers, i.e. they
// are the real number of bytes written to disk.
type State struct {
	BytesClosed  int64  `json:"bytes_closed"`  // bytes in files that were completed
	CurrentFile  string `json:"current_file"`  // file being written ("" if none)
	CurrentBytes int64  `json:"current_bytes"` // informative; real size wins on restart
	TotalBytes   int64  `json:"total_bytes"`   // BytesClosed + CurrentBytes
	FileSeq      int    `json:"file_seq"`      // last sequence number used
	Eligible     uint64 `json:"eligible"`      // eligible packets seen (sampling counter)
	Kept         uint64 `json:"kept"`          // packets written
	Stage        int64  `json:"stage"`         // current "1 of N"
	Stopped      bool   `json:"stopped"`       // max reached, capture finished
	Updated      string `json:"updated"`
}

type sampler struct {
	dir, statePath           string
	rotate, stageBytes, maxB int64
	snaplen                  int
	all                      bool
	nets                     []*net.IPNet

	st  State
	f   *os.File
	w   *bufio.Writer
	cur int64 // bytes in the current file (incl. buffered)
}

func main() {
	s := &sampler{}
	var netsFlag string
	flag.StringVar(&s.dir, "dir", "/mnt/captures", "output directory for pcap files")
	flag.StringVar(&s.statePath, "state", "", "state file (default <dir>/pcapsampler-state.json)")
	flag.Int64Var(&s.rotate, "rotate", 100_000_000, "rotate files after this many bytes (100 MB)")
	flag.Int64Var(&s.stageBytes, "stage-bytes", 1_000_000_000, "bytes per sampling stage (1 GB)")
	flag.Int64Var(&s.maxB, "max", 6_000_000_000, "stop capturing when this many bytes are stored (6 GB)")
	flag.IntVar(&s.snaplen, "snaplen", 65535, "bytes stored per packet")
	flag.BoolVar(&s.all, "all", false, "store every received frame, not only inter-node IPv4")
	flag.StringVar(&netsFlag, "nets", "203.0.113.0/24,198.51.100.0/24,100.64.0.0/10",
		"comma separated lab ranges; a packet is inter-node when src and dst are inside")
	status := flag.Bool("status", false, "print the state file and exit")
	flag.Parse()
	if s.statePath == "" {
		s.statePath = filepath.Join(s.dir, "pcapsampler-state.json")
	}
	for _, c := range strings.Split(netsFlag, ",") {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			log.Fatalf("bad -nets entry %q: %v", c, err)
		}
		s.nets = append(s.nets, n)
	}
	if *status {
		b, err := os.ReadFile(s.statePath)
		if err != nil {
			log.Fatal(err)
		}
		os.Stdout.Write(b)
		return
	}
	log.SetPrefix("pcapsampler: ")
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		log.Fatal(err)
	}
	// Refuse to run twice on the same directory (two writers would corrupt the log).
	lk, err := os.OpenFile(filepath.Join(s.dir, ".pcapsampler.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		log.Fatalf("another pcapsampler is using %s: %v", s.dir, err)
	}
	if err := s.recover(); err != nil {
		log.Fatalf("recover: %v", err)
	}
	if s.st.Stopped || s.total() >= s.maxB {
		s.finish()
		return
	}
	if err := s.run(); err != nil {
		log.Fatalf("run: %v", err)
	}
}

func (s *sampler) total() int64 { return s.st.BytesClosed + s.cur }

func (s *sampler) stage() int64 { return 1 + s.total()/s.stageBytes }

// recover loads the state file (if any) and reconciles it with the files on disk.
func (s *sampler) recover() error {
	b, err := os.ReadFile(s.statePath)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.st); err != nil {
			return fmt.Errorf("state file %s is corrupt (%v); fix or remove it", s.statePath, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// First run (or lost state): everything already on disk counts as stored.
		files, _ := filepath.Glob(filepath.Join(s.dir, filePrefix+"*"+fileSuffix))
		sort.Strings(files)
		for _, f := range files {
			if fi, err := os.Stat(f); err == nil {
				s.st.BytesClosed += fi.Size()
			}
			var seq int
			fmt.Sscanf(filepath.Base(f), filePrefix+"%06d", &seq)
			if seq > s.st.FileSeq {
				s.st.FileSeq = seq
			}
		}
		if len(files) > 0 {
			// Treat the newest file as "current" so a torn tail gets repaired.
			last := files[len(files)-1]
			fi, _ := os.Stat(last)
			s.st.BytesClosed -= fi.Size()
			s.st.CurrentFile = filepath.Base(last)
		}
	default:
		return err
	}
	// The file that was open at shutdown/power cut is closed now: repair its
	// tail and account its real size. A new file is always started.
	if s.st.CurrentFile != "" {
		p := filepath.Join(s.dir, s.st.CurrentFile)
		size, err := repairPcap(p)
		if err != nil {
			log.Printf("warning: cannot repair %s: %v", p, err)
		}
		s.st.BytesClosed += size
		s.st.CurrentFile, s.st.CurrentBytes = "", 0
	}
	s.cur = 0
	log.Printf("resumed: stored=%d bytes, files=%d, eligible=%d kept=%d stage=1/%d stopped=%v",
		s.total(), s.st.FileSeq, s.st.Eligible, s.st.Kept, s.stage(), s.st.Stopped)
	return s.saveState()
}

// repairPcap truncates a pcap file to its last complete record and returns its size.
// Files too short to hold a global header are removed (size 0).
func repairPcap(path string) (int64, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if fi.Size() < globalHdrLen {
		f.Close()
		return 0, os.Remove(path)
	}
	r := bufio.NewReaderSize(f, 1<<20)
	if _, err := r.Discard(globalHdrLen); err != nil {
		return 0, err
	}
	good := int64(globalHdrLen)
	var hdr [recordHdrLen]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			break
		}
		incl := int64(binary.LittleEndian.Uint32(hdr[8:12]))
		if incl > 1<<20 {
			break // garbage
		}
		if _, err := r.Discard(int(incl)); err != nil {
			break
		}
		good += recordHdrLen + incl
	}
	if good != fi.Size() {
		log.Printf("repairing %s: truncating %d -> %d bytes", path, fi.Size(), good)
		if err := f.Truncate(good); err != nil {
			return fi.Size(), err
		}
		f.Sync()
	}
	return good, nil
}

func (s *sampler) saveState() error {
	s.st.CurrentBytes = s.cur
	s.st.TotalBytes = s.total()
	s.st.Stage = s.stage()
	s.st.Updated = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.MarshalIndent(&s.st, "", "  ")
	tmp := s.statePath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, s.statePath)
}

func (s *sampler) openNext() error {
	s.st.FileSeq++
	name := fmt.Sprintf("%s%06d-%s%s", filePrefix, s.st.FileSeq,
		time.Now().UTC().Format("20060102T150405Z"), fileSuffix)
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	s.f, s.w = f, bufio.NewWriterSize(f, 1<<20)
	var h [globalHdrLen]byte
	binary.LittleEndian.PutUint32(h[0:], pcapMagicNS)
	binary.LittleEndian.PutUint16(h[4:], 2) // version 2.4
	binary.LittleEndian.PutUint16(h[6:], 4)
	binary.LittleEndian.PutUint32(h[16:], uint32(s.snaplen))
	binary.LittleEndian.PutUint32(h[20:], linktypeEther)
	s.w.Write(h[:])
	s.cur = globalHdrLen
	s.st.CurrentFile = name
	log.Printf("writing %s (stored so far %d bytes, stage 1/%d)", name, s.total(), s.stage())
	return s.saveState()
}

// flush pushes buffered data to stable storage, then records the state.
func (s *sampler) flush() error {
	if s.w != nil {
		if err := s.w.Flush(); err != nil {
			return err
		}
		if err := s.f.Sync(); err != nil {
			return err
		}
	}
	return s.saveState()
}

func (s *sampler) closeCurrent() error {
	if s.f == nil {
		return nil
	}
	if err := s.w.Flush(); err != nil {
		return err
	}
	s.f.Sync()
	s.f.Close()
	s.st.BytesClosed += s.cur
	s.cur, s.f, s.w, s.st.CurrentFile = 0, nil, nil, ""
	return s.saveState()
}

func (s *sampler) write(ts time.Time, pkt []byte, origLen int) error {
	capLen := len(pkt)
	if capLen > s.snaplen {
		capLen = s.snaplen
	}
	rec := int64(recordHdrLen + capLen)
	if s.f != nil && s.cur+rec > s.rotate {
		if err := s.closeCurrent(); err != nil {
			return err
		}
	}
	if s.f == nil {
		if err := s.openNext(); err != nil {
			return err
		}
	}
	var h [recordHdrLen]byte
	binary.LittleEndian.PutUint32(h[0:], uint32(ts.Unix()))
	binary.LittleEndian.PutUint32(h[4:], uint32(ts.Nanosecond()))
	binary.LittleEndian.PutUint32(h[8:], uint32(capLen))
	binary.LittleEndian.PutUint32(h[12:], uint32(origLen))
	s.w.Write(h[:])
	_, err := s.w.Write(pkt[:capLen])
	s.cur += rec
	s.st.Kept++
	return err
}

// interNode reports whether an Ethernet frame carries IPv4 between two lab addresses.
func (s *sampler) interNode(frame []byte) bool {
	if len(frame) < 14 {
		return false
	}
	off := 12
	et := binary.BigEndian.Uint16(frame[off:])
	if et == ethPVLAN && len(frame) >= 18 {
		off += 4
		et = binary.BigEndian.Uint16(frame[off:])
	}
	ip := frame[off+2:]
	if et != ethPIPv4 || len(ip) < 20 || ip[0]>>4 != 4 {
		return false
	}
	src, dst := net.IP(ip[12:16]), net.IP(ip[16:20])
	return s.inLab(src) && s.inLab(dst)
}

func (s *sampler) inLab(ip net.IP) bool {
	for _, n := range s.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func (s *sampler) run() error {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(ethPAll)))
	if err != nil {
		return fmt.Errorf("AF_PACKET socket (needs CAP_NET_RAW): %w", err)
	}
	// ifindex 0 = every interface of this namespace, including veths created later.
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: htons(ethPAll)}); err != nil {
		return err
	}
	syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUFFORCE, 32<<20)
	syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_TIMESTAMPNS, 1)
	// Wake up periodically so state is saved even when the network is idle.
	tv := syscall.Timeval{Sec: 0, Usec: 250000}
	syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	buf := make([]byte, 256<<10)
	oob := make([]byte, 128)
	lastSave := time.Now()
	log.Printf("capturing (rotate=%d stage=%d max=%d snaplen=%d all=%v)",
		s.rotate, s.stageBytes, s.maxB, s.snaplen, s.all)
	for {
		select {
		case <-sig:
			log.Printf("signal: closing")
			s.closeCurrent()
			return s.saveState()
		default:
		}
		if time.Since(lastSave) >= stateSavePeriod {
			if err := s.flush(); err != nil {
				return err
			}
			lastSave = time.Now()
		}
		// MSG_TRUNC makes n the real frame length even if it exceeds buf.
		n, oobn, _, from, err := syscall.Recvmsg(fd, buf, oob, syscall.MSG_TRUNC)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EINTR {
				continue
			}
			return err
		}
		ll, ok := from.(*syscall.SockaddrLinklayer)
		if !ok || ll.Pkttype == syscall.PACKET_OUTGOING {
			continue // count each forwarded packet once: on ingress
		}
		got := n
		if got > len(buf) {
			got = len(buf)
		}
		frame := buf[:got]
		if !s.all && !s.interNode(frame) {
			continue
		}
		s.st.Eligible++
		stage := s.stage()
		if (s.st.Eligible-1)%uint64(stage) != 0 {
			continue
		}
		if err := s.write(rxTime(oob[:oobn]), frame, n); err != nil {
			return err
		}
		if s.total() >= s.maxB {
			s.closeCurrent()
			s.finish()
			return nil
		}
	}
}

// rxTime extracts the SO_TIMESTAMPNS control message, falling back to now.
func rxTime(oob []byte) time.Time {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err == nil {
		for _, m := range msgs {
			if m.Header.Level == syscall.SOL_SOCKET && m.Header.Type == syscall.SO_TIMESTAMPNS &&
				len(m.Data) >= int(unsafe.Sizeof(syscall.Timespec{})) {
				ts := (*syscall.Timespec)(unsafe.Pointer(&m.Data[0]))
				return time.Unix(ts.Sec, ts.Nsec)
			}
		}
	}
	return time.Now()
}

// finish marks the log as complete and blocks forever, so a supervisor with
// "respawn" does not restart the capture in a loop.
func (s *sampler) finish() {
	s.st.Stopped = true
	s.saveState()
	log.Printf("limit reached: %d bytes stored (max %d). Capture stopped permanently.", s.total(), s.maxB)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
}
