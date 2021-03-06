package message

import (
	"bytes"
	"testing"

	proto "github.com/ipfs/go-ipfs/Godeps/_workspace/src/code.google.com/p/goprotobuf/proto"

	blocks "github.com/ipfs/go-ipfs/blocks"
	pb "github.com/ipfs/go-ipfs/exchange/bitswap/message/internal/pb"
	u "github.com/ipfs/go-ipfs/util"
)

func TestAppendWanted(t *testing.T) {
	const str = "foo"
	m := New()
	m.AddEntry(u.Key(str), 1)

	if !wantlistContains(m.ToProto().GetWantlist(), str) {
		t.Fail()
	}
	m.ToProto().GetWantlist().GetEntries()
}

func TestNewMessageFromProto(t *testing.T) {
	const str = "a_key"
	protoMessage := new(pb.Message)
	protoMessage.Wantlist = new(pb.Message_Wantlist)
	protoMessage.Wantlist.Entries = []*pb.Message_Wantlist_Entry{
		&pb.Message_Wantlist_Entry{Block: proto.String(str)},
	}
	if !wantlistContains(protoMessage.Wantlist, str) {
		t.Fail()
	}
	m := newMessageFromProto(*protoMessage)
	if !wantlistContains(m.ToProto().GetWantlist(), str) {
		t.Fail()
	}
}

func TestAppendBlock(t *testing.T) {

	strs := make([]string, 2)
	strs = append(strs, "Celeritas")
	strs = append(strs, "Incendia")

	m := New()
	for _, str := range strs {
		block := blocks.NewBlock([]byte(str))
		m.AddBlock(block)
	}

	// assert strings are in proto message
	for _, blockbytes := range m.ToProto().GetBlocks() {
		s := bytes.NewBuffer(blockbytes).String()
		if !contains(strs, s) {
			t.Fail()
		}
	}
}

func TestWantlist(t *testing.T) {
	keystrs := []string{"foo", "bar", "baz", "bat"}
	m := New()
	for _, s := range keystrs {
		m.AddEntry(u.Key(s), 1)
	}
	exported := m.Wantlist()

	for _, k := range exported {
		present := false
		for _, s := range keystrs {

			if s == string(k.Key) {
				present = true
			}
		}
		if !present {
			t.Logf("%v isn't in original list", k.Key)
			t.Fail()
		}
	}
}

func TestCopyProtoByValue(t *testing.T) {
	const str = "foo"
	m := New()
	protoBeforeAppend := m.ToProto()
	m.AddEntry(u.Key(str), 1)
	if wantlistContains(protoBeforeAppend.GetWantlist(), str) {
		t.Fail()
	}
}

func TestToNetFromNetPreservesWantList(t *testing.T) {
	original := New()
	original.AddEntry(u.Key("M"), 1)
	original.AddEntry(u.Key("B"), 1)
	original.AddEntry(u.Key("D"), 1)
	original.AddEntry(u.Key("T"), 1)
	original.AddEntry(u.Key("F"), 1)

	var buf bytes.Buffer
	if err := original.ToNet(&buf); err != nil {
		t.Fatal(err)
	}

	copied, err := FromNet(&buf)
	if err != nil {
		t.Fatal(err)
	}

	keys := make(map[u.Key]bool)
	for _, k := range copied.Wantlist() {
		keys[k.Key] = true
	}

	for _, k := range original.Wantlist() {
		if _, ok := keys[k.Key]; !ok {
			t.Fatalf("Key Missing: \"%v\"", k)
		}
	}
}

func TestToAndFromNetMessage(t *testing.T) {

	original := New()
	original.AddBlock(blocks.NewBlock([]byte("W")))
	original.AddBlock(blocks.NewBlock([]byte("E")))
	original.AddBlock(blocks.NewBlock([]byte("F")))
	original.AddBlock(blocks.NewBlock([]byte("M")))

	var buf bytes.Buffer
	if err := original.ToNet(&buf); err != nil {
		t.Fatal(err)
	}

	m2, err := FromNet(&buf)
	if err != nil {
		t.Fatal(err)
	}

	keys := make(map[u.Key]bool)
	for _, b := range m2.Blocks() {
		keys[b.Key()] = true
	}

	for _, b := range original.Blocks() {
		if _, ok := keys[b.Key()]; !ok {
			t.Fail()
		}
	}
}

func wantlistContains(wantlist *pb.Message_Wantlist, x string) bool {
	for _, e := range wantlist.GetEntries() {
		if e.GetBlock() == x {
			return true
		}
	}
	return false
}

func contains(strs []string, x string) bool {
	for _, s := range strs {
		if s == x {
			return true
		}
	}
	return false
}

func TestDuplicates(t *testing.T) {
	b := blocks.NewBlock([]byte("foo"))
	msg := New()

	msg.AddEntry(b.Key(), 1)
	msg.AddEntry(b.Key(), 1)
	if len(msg.Wantlist()) != 1 {
		t.Fatal("Duplicate in BitSwapMessage")
	}

	msg.AddBlock(b)
	msg.AddBlock(b)
	if len(msg.Blocks()) != 1 {
		t.Fatal("Duplicate in BitSwapMessage")
	}
}
