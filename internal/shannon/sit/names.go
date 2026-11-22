package sit

import "fmt"

var names = map[uint16]string{
	CallList: "GET_CURRENT_CALLS", SMSAck: "SEND_SMS_ACK", SMSCAddress: "GET_SMSC_ADDR",
	IndNewSMS: "IND_NEW_SMS", IndNewSMSReport: "IND_NEW_SMS_STATUS_REPORT", SIMStatus: "GET_SIM_STATUS",
	VerifyPIN: "VERIFY_SIM_PIN", VerifyPUK: "VERIFY_SIM_PUK", SIMIO: "SIM_IO",
	IndSIMStatus: "IND_SIM_STATUS_CHANGED", GetSlotStatus: "GET_SLOT_STATUS", SetSlotMapping: "SET_LOGICAL_SLOT_MAPPING", GetIMSI: "GET_IMSI", SetupDataCall: "SETUP_DATA_CALL",
	DeactDataCall: "DEACT_DATA_CALL", DataCallList: "GET_DATA_CALL_LIST",
	InitialAttachAPN: "SET_INITIAL_ATTACH_APN", IndDataCallList: "IND_DATA_CALL_LIST_CHANGED",
	SetDataProfile: "SET_DATA_PROFILE", SetMobileData: "SET_MOBILE_DATA_STATE",
	CSRegState: "GET_CS_REG_STATE", PSRegState: "GET_PS_REG_STATE", Operator: "GET_OPERATOR",
	IndNetworkState: "IND_NTW_STATE_CHANGED", SetPSService: "SET_PS_SERVICE", GetPSService: "GET_PS_SERVICE",
	IndPhysicalChannel: "IND_PHYSICAL_CHANNEL_CONFIG", SetNRMode: "SET_NR_MODE", GetNRMode: "GET_NR_MODE",
	IndRegFailed: "IND_REG_FAILED", SetRadioPower: "SET_RADIO_POWER", GetRadioPower: "GET_RADIO_POWER",
	IndSlotStatus: "IND_SLOT_STATUS_CHANGED", IndRadioState: "IND_RADIO_STATE_CHANGED", IndPhoneReset: "IND_PHONE_RESET",
	SetScreenState: "SET_SCREEN_STATE", IndSignal: "IND_SIGNAL_STRENGTH", SetIndFilter: "SET_IND_CMD_FILTER",
	Dial: "AIMS_DIAL", IndCallRing: "IND_AIMS_CALL_RING", Answer: "AIMS_ANSWER", Hangup: "AIMS_HANGUP",
	IndCallStatus: "IND_AIMS_CALL_STATUS", IndIMSRegistration: "IND_AIMS_REGISTRATION",
	HiddenMenu: "AIMS_HIDDEN_MENU", AddPDNInfo: "AIMS_ADD_PDN_INFO", SendDTMF: "AIMS_SEND_DTMF",
	SendSMS: "AIMS_SEND_SMS", IMSSMSAck: "AIMS_SEND_SMS_ACK", IndIMSNewSMS: "IND_AIMS_NEW_SMS",
	IndIMSSMSReport: "IND_AIMS_NEW_SMS_STATUS_REPORT", DelPDNInfo: "AIMS_DEL_PDN_INFO",
	IMSStackStart: "AIMS_STACK_START_REQ", IndCarrierStatus: "OEM_IND_CARRIER_CONFIG_STATUS",
	IndCarrierSIMInfo: "OEM_IND_CARRIER_CONFIG_SIM_INFO",
}

func Name(id uint16) string {
	if n, ok := names[id]; ok {
		return n
	}
	return fmt.Sprintf("0x%04x", id)
}
