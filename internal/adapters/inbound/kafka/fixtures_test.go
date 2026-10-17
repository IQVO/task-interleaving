package kafka_test

import (
	"strconv"
	"strings"
)

// The consumed messages are the producers' own contracts as published in their
// AsyncAPI (and pinned in ADR 0003), restated here as raw JSON, never built
// with this service's helper, which only produces this service's own types.

func ce(id, source, typ, subject, dataschema, data string) []byte {
	return []byte(`{"specversion":"1.0","id":"` + id + `","source":"` + source + `","type":"` + typ + `","subject":"` + subject + `",` +
		`"time":"2026-10-17T09:00:00Z","datacontenttype":"application/json","dataschema":"` + dataschema + `","data":` + data + `}`)
}

const (
	typeAvailable = "com.warehouse.wes.fulfillment-execution.task.TaskAvailable"
	typeClaimed   = "com.warehouse.wes.fulfillment-execution.task.TaskClaimed"
	typeCompleted = "com.warehouse.wes.fulfillment-execution.task.TaskCompleted"
	typeZone      = "com.warehouse.wms.facility-layout.zone.ZoneRegistered"
	typeLocation  = "com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered"
	typeDecom     = "com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned"
	typeGeometry  = "com.warehouse.wms.facility-layout.locationslot.LocationGeometryUpdated"
)

func fulfillmentMsg(id, typ, subject, data string) []byte {
	name := typ[strings.LastIndex(typ, ".")+1:]
	return ce(id, "/warehouse/fulfillment-execution", typ, subject, "urn:warehouse:fulfillment-execution:events:"+name+":v1", data)
}

// availableMsg is a TaskAvailable of site S1 for a PICK task going to and
// ending at the same location.
func availableMsg(id, task, location string) []byte {
	data := `{"task_id":"` + task + `","task_type":"PICK","site_id":"S1","from_location":"` + location + `","to_location":"` + location + `",` +
		`"required_capabilities":["PICK"],"priority_band":"P1","available_at":"2026-10-17T08:59:00Z"}`
	return fulfillmentMsg(id, typeAvailable, task, data)
}

func claimedMsg(id, task, station, claimedAt string) []byte {
	data := `{"task_id":"` + task + `","task_type":"PICK","station_id":"` + station + `"`
	if claimedAt != "" {
		data += `,"claimed_at":"` + claimedAt + `"`
	}
	return fulfillmentMsg(id, typeClaimed, task, data+`}`)
}

// completedMsg carries the fields the producer adds next to the two this
// service reads; they are ignored.
func completedMsg(id, task, station string) []byte {
	data := `{"task_id":"` + task + `","station_id":"` + station + `","work_unit_id":"wu-1","task_type":"PICK","quantity":3}`
	return fulfillmentMsg(id, typeCompleted, task, data)
}

func facilityMsg(id, typ, subject, data string) []byte {
	name := typ[strings.LastIndex(typ, ".")+1:]
	return ce(id, "/warehouse/facility-layout", typ, subject, "urn:warehouse:facility-layout:events:"+name+":v1", data)
}

func zoneMsg(id, zoneID, siteCode string) []byte {
	return facilityMsg(id, typeZone, zoneID, `{"zoneId":"`+zoneID+`","siteCode":"`+siteCode+`","areaCode":"A1","zoneCode":"FWD","temperatureClass":"Ambient","hazmat":false}`)
}

func locationMsg(id, code, zoneID string) []byte {
	return facilityMsg(id, typeLocation, code, `{"locationCode":"`+code+`","aisleId":"A-01","zoneId":"`+zoneID+`","locationType":"Slot","maxWeightKg":40,"maxVolumeM3":0.5}`)
}

func decommissionedMsg(id, code string) []byte {
	return facilityMsg(id, typeDecom, code, `{"locationCode":"`+code+`"}`)
}

func geometryMsg(id, code string, x, y, z int) []byte {
	data := `{"locationCode":"` + code + `","xM":` + strconv.Itoa(x) + `,"yM":` + strconv.Itoa(y) + `,"zM":` + strconv.Itoa(z) +
		`,"widthM":1,"depthM":1,"heightM":1,"pickSequence":7}`
	return facilityMsg(id, typeGeometry, code, data)
}

// legacyFlat is the retired flat envelope; a consumer must reject it.
const legacyFlat = `{"event_id":"e-1","event_type":"TaskCompleted","occurred_at":"2026-10-17T09:00:00Z","task_id":"T0","station_id":"STN-1"}`
