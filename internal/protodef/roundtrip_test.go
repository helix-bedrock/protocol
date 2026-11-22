package protodef

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"maps"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/helix-bedrock/protocol"
	"github.com/helix-bedrock/protocol/packet"
)

const testShieldID = 355

var (
	schemaOnce sync.Once
	schemaVal  *Schema
	schemaJSON map[string]any
	schemaErr  error
)

// testSchema generates the schema once for all tests and reads it back from JSON, so
// the tests check the JSON as written.
func testSchema(t *testing.T) (*Schema, map[string]any) {
	t.Helper()
	schemaOnce.Do(func() {
		schemaVal, schemaErr = Generate(filepath.Join("..", ".."))
		if schemaErr != nil {
			return
		}
		var data []byte
		if data, schemaErr = json.Marshal(schemaVal); schemaErr != nil {
			return
		}
		var doc struct {
			Types map[string]any `json:"types"`
		}
		schemaErr = json.Unmarshal(data, &doc)
		schemaJSON = doc.Types
	})
	if schemaErr != nil {
		t.Fatal(schemaErr)
	}
	return schemaVal, schemaJSON
}

func TestGenerateHasNoDiagnostics(t *testing.T) {
	s, _ := testSchema(t)
	for _, d := range s.Diagnostics {
		t.Error(d)
	}
}

// TestSchemaRefsResolve checks that every type a schema names is defined.
func TestSchemaRefsResolve(t *testing.T) {
	_, types := testSchema(t)
	builtin := map[string]bool{
		"void": true, "bool": true, "u8": true, "i8": true, "li16": true, "lu16": true, "li32": true, "lu32": true,
		"i32": true, "li64": true, "lu64": true, "lf32": true, "lf64": true, "varint": true, "varint128": true,
		"container": true, "array": true, "option": true, "switch": true, "mapper": true, "pstring": true, "buffer": true,
	}
	var missing []string
	var walk func(where string, t any)
	walk = func(where string, t any) {
		switch t := t.(type) {
		case string:
			if !builtin[t] {
				if _, ok := types[t]; !ok {
					missing = append(missing, where+": "+t)
				}
			}
		case []any:
			kind, arg := typeKind(t)
			switch kind {
			case "container":
				for _, f := range arg.([]any) {
					walk(where, f.(map[string]any)["type"])
				}
			case "array", "mapper":
				walk(where, arg.(map[string]any)["type"])
			case "option":
				walk(where, arg)
			case "switch":
				opts := arg.(map[string]any)
				for _, c := range opts["fields"].(map[string]any) {
					walk(where, c)
				}
				if d, ok := opts["default"]; ok {
					walk(where, d)
				}
			case "encapsulated":
				opts := arg.(map[string]any)
				walk(where, opts["type"])
				walk(where, opts["lengthType"])
			}
		}
	}
	for name, def := range types {
		if def != "native" {
			walk(name, def)
		}
	}
	for _, m := range missing {
		t.Errorf("undefined type %s", m)
	}
}

// TestRoundTrip checks the schema against the Go code for every packet, in both
// directions:
//
//  1. a random value of the packet's schema type is encoded, and the Reader must read
//     it without error (other than rejecting a value) and without bytes left over;
//  2. the values the schema decodes from those bytes must be the ones the Reader stored
//     in the Go struct, which catches a field read with the wrong type of the same
//     size, such as li32 for lf32;
//  3. the packet the Reader read is written again by the Writer, and the schema must
//     decode those bytes, again with nothing left over, to the same values except for
//     the fields in rewrittenFields, which the Writer derives from other values.
//
// Values are random but steered: a switch picks one of its cases, and a count matches
// its array. Every switch case, option and non-empty array of the schema must be
// reached by a sample the Reader accepts, except those in unreachable.
func TestRoundTrip(t *testing.T) {
	s, types := testSchema(t)
	in := &interp{types: types, vars: map[string]any{"ShieldItemID": int64(testShieldID)}}
	pool := map[uint32]func() packet.Packet{}
	for _, p := range []packet.Pool{packet.NewServerPool(), packet.NewClientPool()} {
		maps.Copy(pool, p)
	}
	cov := &coverage{taken: map[string]map[string]bool{}, rewrites: map[string]int{}}
	// Cleanup, not defer: the parallel subtests only run once this function returns.
	t.Cleanup(func() { cov.report(t, types) })
	for _, pk := range s.Packets {
		t.Run(pk.Name, func(t *testing.T) {
			t.Parallel()
			newPacket, ok := pool[pk.ID]
			if !ok {
				t.Fatalf("packet %d not in any pool", pk.ID)
			}
			taken, rewrites := map[string]map[string]bool{}, map[string]int{}
			defer cov.merge(taken, rewrites)
			roundTrip(t, s, in, "packet_"+pk.Name, newPacket, rand.New(rand.NewPCG(uint64(pk.ID), 1)), taken, rewrites)
		})
	}
}

// TestRoundTrip tries at least minSamples random values per packet, and more, up to
// maxSamples, until the samples the Reader accepts reach every branch of the packet's
// types. Rare branches (such as a skin whose image sizes happen to agree) need them.
const (
	minSamples = 400
	maxSamples = 5000
)

func roundTrip(t *testing.T, s *Schema, in *interp, typ string, newPacket func() packet.Packet, rng *rand.Rand, taken map[string]map[string]bool, rewrites map[string]int) {
	accepted, generated := 0, 0
	var firstReject error
	want := branches(in, typ)
	for i := 0; i < maxSamples && (i < minSamples || !reachedAll(taken, want)); i++ {
		g := &generator{interp: in, rng: rng, fixed: map[*obj]map[string]bool{}, values: s.Values}
		v := g.gen(typ, nil)
		if g.conflict {
			continue
		}
		enc := &encoder{interp: in}
		if err := enc.write(typ, v, nil); err != nil {
			t.Fatalf("encode generated value: %v", err)
		}
		generated++
		data := enc.buf.Bytes()

		// 1: schema -> Reader.
		buf := bytes.NewBuffer(data)
		read := newPacket()
		r := protocol.NewReader(buf, testShieldID, true)
		read.Marshal(r)
		if err := r.Err(); err != nil {
			// The Reader may reject a value the schema cannot rule out (an unknown enum
			// value, a string too long), but any other error means the bytes did not
			// line up with what the Reader expects.
			if !isRejection(err) {
				t.Fatalf("Reader failed on what the schema wrote: %v\n%s", err, hex.Dump(data))
			}
			if firstReject == nil {
				firstReject = err
			}
			continue
		}
		if buf.Len() != 0 {
			t.Fatalf("Reader left %d of %d bytes the schema wrote\n%s", buf.Len(), len(data), hex.Dump(data))
		}
		accepted++

		// 2: the values the schema decodes are the ones the Reader stored. The decode
		// also records what the sample reached.
		dec := &decoder{interp: in, buf: data, taken: taken}
		genVal, err := dec.read(typ, nil)
		if err != nil || dec.off != len(data) {
			t.Fatalf("schema cannot decode what it wrote (%v)\n%s", err, hex.Dump(data))
		}
		if o, ok := genVal.(*obj); ok {
			cmp := &comparer{in: in, goFields: s.goFields}
			cmp.object(typ, o, reflect.ValueOf(read), typ)
			if len(cmp.errs) > 0 {
				t.Fatalf("the schema decodes other values than the Reader:\n%s\n%s", strings.Join(cmp.errs, "\n"), hex.Dump(data))
			}
		}

		// 3: Writer -> schema.
		var out bytes.Buffer
		w := protocol.NewWriter(&out, testShieldID)
		read.Marshal(w)
		if err := w.Err(); err != nil {
			t.Fatalf("Writer failed on a packet the Reader read: %v", err)
		}
		written := out.Bytes()
		if bytes.Equal(written, data) {
			// Decoded and checked above already.
			continue
		}
		wdec := &decoder{interp: in, buf: written, taken: taken}
		wroteVal, err := wdec.read(typ, nil)
		if err != nil {
			t.Fatalf("schema cannot decode what the Writer wrote: %v\nwritten:\n%sgenerated:\n%s", err, hex.Dump(written), hex.Dump(data))
		}
		if wdec.off != len(written) {
			t.Fatalf("schema left %d of %d bytes the Writer wrote\n%s", len(written)-wdec.off, len(written), hex.Dump(written))
		}
		diffs := map[string]bool{}
		diffValues(genVal, wroteVal, "", diffs)
		for _, path := range keysOf(boolMap(diffs)) {
			entry, ok := rewritten(path)
			if !ok {
				t.Fatalf("the Writer wrote %s back with another value: the schema may read it with the wrong type\ngenerated:\n%swritten:\n%s", path, hex.Dump(data), hex.Dump(written))
			}
			rewrites[entry]++
		}
	}
	if testing.Verbose() {
		t.Logf("%d of %d generated samples accepted; first rejection: %v", accepted, generated, firstReject)
	}
	if accepted == 0 {
		t.Fatalf("the Reader accepted none of %d generated samples; first error: %v", generated, firstReject)
	}
}

// coverage collects what the samples of all packets reach.
type coverage struct {
	mu       sync.Mutex
	taken    map[string]map[string]bool
	rewrites map[string]int
}

func (c *coverage) merge(taken map[string]map[string]bool, rewrites map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for site, branches := range taken {
		if c.taken[site] == nil {
			c.taken[site] = map[string]bool{}
		}
		maps.Copy(c.taken[site], branches)
	}
	for entry, n := range rewrites {
		c.rewrites[entry] += n
	}
}

// report fails for every switch case, option or non-empty array that no sample the
// Reader accepted reached, unless unreachable says why none can. On a run of every
// packet, it also fails for allow-list entries that no longer hold, so they cannot
// hide a later regression.
func (c *coverage) report(t *testing.T, types map[string]any) {
	if t.Failed() {
		return
	}
	full := !strings.Contains(flag.Lookup("test.run").Value.String(), "/")
	total, hit := 0, 0
	labels := map[string]bool{}
	reached := func(label, site, branch string) bool {
		labels[label] = true
		total++
		if c.taken[site][branch] {
			hit++
			if _, listed := unreachable[label]; listed && full {
				t.Errorf("%s is reached now: remove it from unreachable", label)
			}
			return true
		}
		if reason, listed := unreachable[label]; listed {
			t.Logf("not reached: %s (%s)", label, reason)
		} else {
			t.Errorf("no sample the Reader accepted reached %s", label)
		}
		return false
	}
	var walk func(where string, typ any)
	walk = func(where string, typ any) {
		kind, arg := typeKind(typ)
		switch kind {
		case "container":
			for _, f := range arg.([]any) {
				fm := f.(map[string]any)
				walk(where+"."+fieldLabel(fm), fm["type"])
			}
		case "array":
			opts := arg.(map[string]any)
			if n, fixed := opts["count"].(float64); fixed && n > 0 || reached(where+"[]", arraySite(opts), "nonempty") {
				walk(where+"[]", opts["type"])
			}
		case "mapper":
			walk(where, arg.(map[string]any)["type"])
		case "option":
			if reached(where+"?", optionSite(typ), "present") {
				walk(where, arg)
			}
		case "switch":
			opts := arg.(map[string]any)
			cases := opts["fields"].(map[string]any)
			for _, k := range keysOf(cases) {
				if reached(where+"["+k+"]", switchSite(opts), k) {
					walk(where, cases[k])
				}
			}
			if d, ok := opts["default"]; ok && reached(where+"[default]", switchSite(opts), "default") {
				walk(where, d)
			}
		}
	}
	for _, name := range keysOf(types) {
		// mcpe_packet is not decoded: each packet type is tested on its own.
		if name != "mcpe_packet" {
			walk(name, types[name])
		}
	}
	t.Logf("branches reached: %d of %d", hit, total)
	if !full {
		return
	}
	for label := range unreachable {
		if !labels[label] {
			t.Errorf("unreachable lists %s, which the schema does not have", label)
		}
	}
	for _, f := range rewrittenFields {
		if c.rewrites[f.path] == 0 {
			t.Errorf("rewrittenFields lists %s, which the Writer never rewrote", f.path)
		}
	}
}

// unreachable are the branches (switch cases, options, non-empty arrays) that no sample
// the Reader accepts can reach, with the reason found in the Go code. Every other
// branch must be reached by the round trip.
var unreachable = map[string]string{
	"ScoreboardEntry.(anon)[default]":                       "the Reader rejects variants without a type name",
	"packet_text.(anon)[default]":                           "the Reader rejects an empty message, and only known text types have one",
	"packet_player_update_entity_overrides.(anon)[default]": "the Reader rejects unknown override types",
	"packet_event.event[agent_command]":                     "no event ordinal for this type: the Reader rejects it (events.go lookupEventOrdinal)",
	"packet_event.event[agent_created]":                     "no event ordinal for this type",
	"packet_event.event[pattern_removed]":                   "no event ordinal for this type",
	"packet_event.event[fish_bucketed]":                     "no event ordinal for this type",
	"packet_event.event[pet_died]":                          "no event ordinal for this type",
	"packet_event.event[movement_anomaly]":                  "no event ordinal for this type",
	"packet_event.event[movement_corrected]":                "no event ordinal for this type",
	"packet_event.event[extract_honey]":                     "no event ordinal for this type",
}

// isRejection reports whether err is the Reader refusing a value (UnknownEnumOption,
// InvalidValue), as opposed to failing to read.
func isRejection(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "unknown value '") || strings.HasPrefix(msg, "invalid value '")
}

// TestNamesCompile checks the names node-protodef's ProtoDefCompiler turns into
// JavaScript identifiers: field names, and array and string counts, which it pastes
// into the generated code as they are.
func TestNamesCompile(t *testing.T) {
	_, types := testSchema(t)
	ident := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	var walk func(where string, typ any)
	walk = func(where string, typ any) {
		kind, arg := typeKind(typ)
		switch kind {
		case "container":
			for _, f := range arg.([]any) {
				fm := f.(map[string]any)
				if name, ok := fm["name"].(string); ok {
					// A field called buffer or ctx would hide the compiled reader's parameters.
					if !ident.MatchString(name) || name == "buffer" || name == "ctx" {
						t.Errorf("%s: field name %q", where, name)
					}
				}
				walk(where+"."+fieldLabel(fm), fm["type"])
			}
		case "array", "pstring", "buffer":
			opts, _ := arg.(map[string]any)
			if c, ok := opts["count"].(string); ok && (!ident.MatchString(c) || isUnsafeCountName(c)) {
				t.Errorf("%s: count %q", where, c)
			}
			if kind == "array" {
				walk(where, opts["type"])
			}
		case "mapper":
			walk(where, arg.(map[string]any)["type"])
		case "option":
			walk(where, arg)
		case "switch":
			opts := arg.(map[string]any)
			for _, c := range opts["fields"].(map[string]any) {
				walk(where, c)
			}
			if d, ok := opts["default"]; ok {
				walk(where, d)
			}
		}
	}
	for _, name := range keysOf(types) {
		walk(name, types[name])
	}
}

func boolMap(m map[string]bool) map[string]any {
	out := map[string]any{}
	for k := range m {
		out[k] = nil
	}
	return out
}

// rewrittenFields are the fields that the Writer does not write back as read, as a
// named type and the path of the field in it (a path covers the fields under it), with
// the reason found in the Go code.
var rewrittenFields = []struct {
	path   string
	reason string
}{
	{"Item.extra", "the Writer sends no user data for air"},
	{"ItemInstance.extra", "the Writer sends no user data for air"},
	{"StackRequestItem.extra", "the Writer sends no user data without an identifier"},
	{"ItemInstance.has_stack_id", "has_stack_id is StackNetworkID != 0 (writer.go ItemInstance)"},
	{"ItemInstance.stack_id", "has_stack_id is StackNetworkID != 0 (writer.go ItemInstance)"},
	{"ItemDescriptorCount.aux", "the Writer sends 32767 for an empty descriptor"},
	{"EntityMetadata", "EntityMetadata is a map: the Writer sends it in key order, once per key"},
	{"packet_resource_pack_client_response.name", "the response name is written from the response type"},
	{"packet_player_update_entity_overrides.name", "the type name is written from the type"},
	{"SoundDataUpdate.index", "the index is written from the type"},
	{"packet_inventory_transaction.has_legacy", "has_legacy is derived from LegacyRequestID"},
	{"packet_inventory_transaction.legacy_set_item_slots", "has_legacy is derived from LegacyRequestID"},
	{"packet_text.category_type", "the category is derived from the text type"},
	{"packet_event.event_ordinal", "the ordinal is derived from the event type"},
	{"ScoreboardEntry.type_name", "the type name is written from the identity type"},
	{"AbilityValue.bool_val", "the value not used by the type is sent as zero"},
	{"AbilityValue.float_val", "the value not used by the type is sent as zero"},
	{"StackRequestAction.legacy_id", "the legacy ID is written from the action"},
	{"StackRequestItemDescriptorCount.legacy_id", "the legacy ID is written from the descriptor"},
	{"RecipeUnlockRequirement.present", "present is derived from the unlock context (recipe.go RecipeUnlockRequirement)"},
	{"RecipeUnlockRequirement.ingredients", "present is derived from the unlock context (recipe.go RecipeUnlockRequirement)"},
	{"ColourRGBf", "RGB floats are kept as bytes"},
	{"SoundPosition", "SoundPos is a float position times 8 in a BlockPos"},
	{"ItemStackResponse.container_info", "the optional is present when there are slots"},
	{"Skin.trusted", "Trusted is a bool sent as a string"},
	{"MaterialReducer.mix", "the reader splits mix into ID and metadata lossily (reader.go MaterialReducer)"},
	{"PlayerListEntry.legacy_action", "the legacy action byte is written from the action and not read back"},
}

// rewritten returns the entry of rewrittenFields that covers path: one that names a
// type of the chain, or a field in one and the fields under it.
func rewritten(path string) (string, bool) {
	for _, part := range strings.Split(path, ">") {
		for _, f := range rewrittenFields {
			if part == f.path || strings.HasPrefix(part, f.path+".") || strings.HasPrefix(part, f.path+"[]") {
				return f.path, true
			}
		}
	}
	return "", false
}

// branch is one branch of a switch, option or array of the schema.
type branch struct{ site, name string }

// branches returns every branch in the type typ and the types it uses.
func branches(in *interp, typ string) []branch {
	var out []branch
	seen := map[string]bool{}
	var walk func(t any)
	walk = func(t any) {
		kind, arg := typeKind(t)
		switch kind {
		case "container":
			for _, f := range arg.([]any) {
				walk(f.(map[string]any)["type"])
			}
		case "array":
			opts := arg.(map[string]any)
			if _, fixed := opts["count"].(float64); !fixed {
				out = append(out, branch{arraySite(opts), "nonempty"})
			}
			walk(opts["type"])
		case "mapper":
			walk(arg.(map[string]any)["type"])
		case "option":
			out = append(out, branch{optionSite(t), "present"})
			walk(arg)
		case "switch":
			opts := arg.(map[string]any)
			cases := opts["fields"].(map[string]any)
			for _, k := range keysOf(cases) {
				out = append(out, branch{switchSite(opts), k})
				walk(cases[k])
			}
			if d, ok := opts["default"]; ok {
				out = append(out, branch{switchSite(opts), "default"})
				walk(d)
			}
		default:
			if def, ok := in.lookup(kind); ok && arg == nil && !seen[kind] {
				seen[kind] = true
				walk(def)
			}
		}
	}
	walk(typ)
	return out
}

// reachedAll reports whether taken holds every branch of want.
func reachedAll(taken map[string]map[string]bool, want []branch) bool {
	for _, b := range want {
		if !taken[b.site][b.name] {
			return false
		}
	}
	return true
}
