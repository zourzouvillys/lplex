package pgn

// victronRegisterNames maps known Victron VE.Can register IDs to names.
// Source: canboat, esphome-victron-vedirect, Victron VE.Can registers public doc.
var victronRegisterNames = map[uint16]string{
	0x0100: "Product ID",
	0x0200: "Device Mode",
	0x0201: "Device State",
	0x0205: "Device Off Reason",
	0x031C: "Warning Reason",
	0x031E: "Alarm Reason",
	0x0FFF: "State of Charge",
	0x0FFE: "Time to Go",
	0xED8D: "DC Channel 1 Voltage",
	0xED8E: "DC Channel 1 Power",
	0xED8F: "DC Channel 1 Current",
	0xEDAD: "Load Current",
	0xEDB3: "MPPT Tracker Mode",
	0xEDBB: "Panel Voltage",
	0xEDBC: "Panel Power",
	0xEDBD: "Panel Current",
	0xEDD0: "Max Power Yesterday",
	0xEDD1: "Yield Yesterday",
	0xEDD2: "Max Power Today",
	0xEDD3: "Yield Today",
	0xEDD5: "Charger Voltage",
	0xEDD7: "Charger Current",
	0xEDDA: "Charger Error Code",
	0xEDDB: "Charger Internal Temp",
	0xEDDC: "User Yield",
	0xEDDD: "System Yield",
	0xEDEC: "Battery Temperature",
	0xEDF0: "Battery Max Current",
	0xEDF1: "Battery Type",
	0xEDF6: "Battery Float Voltage",
	0xEDF7: "Battery Absorption Voltage",
	0xEEFF: "Discharge Since Full",
}

// RegisterName returns the human-readable name for this Victron register,
// or empty string if unknown.
func (m VictronBatteryRegister) RegisterName() string {
	return victronRegisterNames[m.RegisterId]
}
