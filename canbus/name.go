package canbus

import "fmt"

// NameFields holds the decoded fields from a 64-bit ISO NAME (PGN 60928).
type NameFields struct {
	NAMEHex          string
	Manufacturer     string
	ManufacturerCode uint16
	DeviceClass      uint8
	DeviceFunction   uint8
	DeviceInstance   uint8
	UniqueNumber     uint32
}

// DecodeNAME parses the 64-bit ISO NAME field from PGN 60928.
//
// NAME field bit layout (64 bits, little-endian):
//
//	bits  0-20:  unique number (21 bits)
//	bits 21-31:  manufacturer code (11 bits)
//	bits 32-34:  device instance lower (3 bits)
//	bits 35-39:  device instance upper (5 bits)
//	bits 40-47:  device function (8 bits)
//	bit  48:     reserved
//	bits 49-55:  device class (7 bits)
//	bits 56-59:  system instance (4 bits)
//	bits 60-62:  industry group (3 bits)
//	bit  63:     arbitrary address capable
func DecodeNAME(name uint64) NameFields {
	uniqueNumber := uint32(name & 0x1FFFFF)
	manufacturerCode := uint16((name >> 21) & 0x7FF)
	instanceLower := uint8((name >> 32) & 0x07)
	instanceUpper := uint8((name >> 35) & 0x1F)
	deviceFunction := uint8((name >> 40) & 0xFF)
	deviceClass := uint8((name >> 49) & 0x7F)

	deviceInstance := (instanceUpper << 3) | instanceLower

	return NameFields{
		NAMEHex:          fmt.Sprintf("%016x", name),
		Manufacturer:     LookupManufacturer(manufacturerCode),
		ManufacturerCode: manufacturerCode,
		DeviceClass:      deviceClass,
		DeviceFunction:   deviceFunction,
		DeviceInstance:   deviceInstance,
		UniqueNumber:     uniqueNumber,
	}
}

// EncodeName builds a 64-bit ISO NAME from individual fields.
// Inverse of DecodeNAME (at the numeric level; manufacturer string is ignored).
//
// Fields map to the same bit layout documented on DecodeNAME:
//
//	bits  0-20:  UniqueNumber (21 bits)
//	bits 21-31:  ManufacturerCode (11 bits)
//	bits 32-34:  DeviceInstance lower 3 bits
//	bits 35-39:  DeviceInstance upper 5 bits
//	bits 40-47:  DeviceFunction (8 bits)
//	bit  48:     reserved (0)
//	bits 49-55:  DeviceClass (7 bits)
//	bits 56-59:  SystemInstance (4 bits)
//	bits 60-62:  IndustryGroup (3 bits)
//	bit  63:     ArbitraryAddressCapable
type NameEncodeFields struct {
	UniqueNumber          uint32
	ManufacturerCode      uint16
	DeviceInstance        uint8
	DeviceFunction        uint8
	DeviceClass           uint8
	SystemInstance        uint8
	IndustryGroup         uint8
	ArbitraryAddressCapable bool
}

// EncodeName encodes the fields into a 64-bit ISO NAME.
func EncodeName(f NameEncodeFields) uint64 {
	var name uint64
	name |= uint64(f.UniqueNumber & 0x1FFFFF)
	name |= uint64(f.ManufacturerCode&0x7FF) << 21
	name |= uint64(f.DeviceInstance&0x07) << 32 // lower 3 bits
	name |= uint64(f.DeviceInstance>>3&0x1F) << 35 // upper 5 bits
	name |= uint64(f.DeviceFunction) << 40
	// bit 48: reserved (0)
	name |= uint64(f.DeviceClass&0x7F) << 49
	name |= uint64(f.SystemInstance&0x0F) << 56
	name |= uint64(f.IndustryGroup&0x07) << 60
	if f.ArbitraryAddressCapable {
		name |= 1 << 63
	}
	return name
}

// LookupManufacturer returns a human-readable manufacturer name for common NMEA 2000 codes.
func LookupManufacturer(code uint16) string {
	if name, ok := Manufacturers[code]; ok {
		return name
	}
	return fmt.Sprintf("Unknown (%d)", code)
}

// Manufacturers maps NMEA 2000 manufacturer codes to names.
var Manufacturers = map[uint16]string{
	69:   "Maretron",
	78:   "FW Murphy",
	80:   "Twin Disc",
	85:   "Kohler",
	88:   "Hemisphere GPS",
	116:  "BEP",
	135:  "Airmar",
	137:  "Simrad",
	140:  "Lowrance",
	144:  "Mercury Marine",
	147:  "Nautibus",
	148:  "Blue Water Data",
	154:  "Westerbeke",
	163:  "Evinrude",
	165:  "CPAC Systems",
	168:  "Xantrex",
	174:  "Yanmar",
	176:  "Mastervolt",
	185:  "BEP Marine",
	192:  "Floscan",
	198:  "Mystic Valley Comms",
	199:  "Actia",
	211:  "Nobeltec",
	215:  "Oceanic Systems",
	224:  "Yacht Monitoring Solutions",
	228:  "ZF",
	229:  "Garmin",
	233:  "Yacht Devices",
	235:  "SilverHook/Fusion",
	243:  "Coelmo",
	257:  "Honda",
	272:  "Groco",
	273:  "Actisense",
	274:  "Amphenol",
	275:  "Navico",
	283:  "Hamilton Jet",
	285:  "Sea Recovery",
	286:  "Coelmo",
	295:  "BEP Marine",
	304:  "Empir Bus",
	305:  "NovAtel",
	306:  "Sleipner",
	315:  "ICOM",
	328:  "Qwerty",
	341:  "Victron Energy",
	345:  "Korea Maritime University",
	351:  "Thrane and Thrane",
	355:  "Mastervolt",
	356:  "Fischer Panda",
	358:  "Victron",
	370:  "Rolls Royce Marine",
	373:  "Electronic Design",
	374:  "Northern Lights",
	378:  "Glendinning",
	381:  "B&G",
	384:  "Rose Point Navigation",
	385:  "Johnson Outdoors",
	394:  "Capi 2",
	396:  "Beyond Measure",
	400:  "Livorsi Marine",
	404:  "ComNav",
	409:  "Chetco Digital Instruments",
	419:  "Fusion",
	421:  "Standard Horizon",
	422:  "True Heading",
	426:  "Egersund Marine Electronics",
	427:  "Em-Trak Marine Electronics",
	431:  "Tohatsu",
	437:  "Digital Yacht",
	440:  "Comar Systems",
	443:  "VDO/Continental",
	451:  "Parker Hannifin",
	459:  "Alltek Marine Electronics",
	460:  "SAN Giorgio",
	466:  "Ocean Signal",
	467:  "Mastervolt",
	470:  "Webasto",
	471:  "Torqeedo",
	473:  "Silvertek",
	476:  "GME/Standard Communications",
	478:  "Humminbird",
	481:  "Sea Cross Marine",
	493:  "LCJ Capteurs",
	499:  "Vesper Marine",
	502:  "Attwood Marine",
	503:  "Naviop",
	504:  "Vessel Systems & Electronics",
	510:  "Marinesoft",
	517:  "NoLand Engineering",
	518:  "Transas Marine",
	529:  "National Instruments Korea",
	532:  "Shenzhen Jiuzhou Himunication",
	540:  "Cummins",
	557:  "Suzuki",
	571:  "Volvo Penta",
	573:  "Watcheye",
	578:  "Advansea",
	579:  "KVH",
	580:  "San Jose Technology",
	583:  "Yacht Control",
	586:  "Ewol",
	591:  "Raymarine",
	595:  "Diverse Yacht Services",
	600:  "Furuno",
	605:  "Si-Tex",
	612:  "Samwon IT",
	614:  "Seekeeper",
	637:  "Cox Powertrain",
	641:  "Humphree",
	644:  "Ocean LED",
	645:  "Prospec",
	658:  "NovAtel",
	688:  "Poly Planar",
	715:  "Lumishore",
	717:  "Bilt Solar",
	735:  "Yamaha",
	739:  "Dometic",
	743:  "Simrad",
	744:  "Intellian",
	773:  "Broyda Industries",
	776:  "Canadian Automotive",
	795:  "Technicold",
	796:  "Blue Water Desalination",
	803:  "Gill Sensors",
	811:  "HelmSmith",
	815:  "Quick",
	824:  "Undheim Systems",
	838:  "TeamSurv",
	845:  "Honda",
	862:  "Oceanvolt",
	868:  "Prospec",
	890:  "Oceanvolt",
	909:  "Still Water Designs",
	911:  "BlueSea",
	1850: "Yamaha",
	1851: "Yamaha",
	1852: "Yamaha",
	1853: "Yamaha",
	1854: "Yamaha",
	1855: "Yamaha",
}
