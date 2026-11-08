package telegram

import (
	"encoding/base64"
	"encoding/binary"
	"sort"
	"strings"
	"time"
)

// What a Telegram id tells by itself: the data center of an account (read
// from a file id of its profile picture, which lives there) and roughly when
// the account was made (ids grow over time).

// FileDC is the data center a file id points to (0 when it cannot be read).
// A file id is URL-safe base64 of a run-length encoded record whose first
// two little-endian int32 are the file type and the data center.
func FileDC(fileID string) int {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(fileID, "="))
	if err != nil {
		return 0
	}
	var out []byte
	zero := false
	for _, c := range raw { // zeros are stored as 0x00 followed by how many
		switch {
		case zero:
			out = append(out, make([]byte, c)...)
			zero = false
		case c == 0:
			zero = true
		default:
			out = append(out, c)
		}
	}
	if len(out) < 8 {
		return 0
	}
	if dc := int(int32(binary.LittleEndian.Uint32(out[4:8]))); dc >= 1 && dc <= 5 {
		return dc
	}
	return 0
}

// DCLocation is where a data center is.
func DCLocation(dc int) string {
	switch dc {
	case 1, 3:
		return "Miami, USA"
	case 2, 4:
		return "Amsterdam, Netherlands"
	case 5:
		return "Singapore"
	}
	return ""
}

// knownAges are user ids whose accounts' creation dates are known (from
// GetIDs, github.com/kraloveckey/getids-bot, MIT): milliseconds since 1970.
var knownAges = map[int64]int64{
	2768409: 1383264000000, 7679610: 1388448000000, 11538514: 1391212000000, 15835244: 1392940000000, 23646077: 1393459000000,
	38015510: 1393632000000, 44634663: 1399334000000, 46145305: 1400198000000, 54845238: 1411257000000, 63263518: 1414454000000,
	101260938: 1425600000000, 101323197: 1426204000000, 111220210: 1429574000000, 103258382: 1432771000000, 103151531: 1433376000000,
	116812045: 1437696000000, 122600695: 1437782000000, 109393468: 1439078000000, 112594714: 1439683000000, 124872445: 1439856000000,
	130029930: 1441324000000, 125828524: 1444003000000, 133909606: 1444176000000, 157242073: 1446768000000, 143445125: 1448928000000,
	148670295: 1452211000000, 152079341: 1453420000000, 171295414: 1457481000000, 181783990: 1460246000000, 222021233: 1465344000000,
	225034354: 1466208000000, 278941742: 1473465000000, 285253072: 1476835000000, 294851037: 1479600000000, 297621225: 1481846000000,
	328594461: 1482969000000, 337808429: 1487707000000, 341546272: 1487782000000, 352940995: 1487894000000, 369669043: 1490918000000,
	400169472: 1501459000000, 805158066: 1563208000000, 1974255900: 1634000000000,
}

type agePoint struct {
	id int64
	at time.Time
}

// agePoints are the known ages by id, their dates made non-decreasing (ids
// are not handed out strictly in order).
var agePoints = func() []agePoint {
	var pts []agePoint
	for id, ms := range knownAges {
		pts = append(pts, agePoint{id, time.UnixMilli(ms).UTC()})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].id < pts[j].id })
	for i := 1; i < len(pts); i++ {
		if pts[i].at.Before(pts[i-1].at) {
			pts[i].at = pts[i-1].at
		}
	}
	return pts
}()

// AccountAge estimates when a user account was made from its id: between
// two known ids the date is interpolated (Approx); before the first known
// one the account is older (Before), after the last one newer (After).
type AccountAge struct {
	When          time.Time
	Approx, After bool
	Before        bool
}

// EstimateAccountAge estimates an account's creation date (users only; zero for chats and bots ids ≤ 0).
func EstimateAccountAge(id int64) (AccountAge, bool) {
	if id <= 0 || len(agePoints) == 0 {
		return AccountAge{}, false
	}
	first, last := agePoints[0], agePoints[len(agePoints)-1]
	switch {
	case id <= first.id:
		return AccountAge{When: first.at, Before: id < first.id, Approx: id == first.id}, true
	case id >= last.id:
		return AccountAge{When: last.at, After: id > last.id, Approx: id == last.id}, true
	}
	i := sort.Search(len(agePoints), func(i int) bool { return agePoints[i].id >= id })
	lo, hi := agePoints[i-1], agePoints[i]
	frac := float64(id-lo.id) / float64(hi.id-lo.id)
	at := lo.at.Add(time.Duration(frac * float64(hi.at.Sub(lo.at))))
	return AccountAge{When: at, Approx: true}, true
}
