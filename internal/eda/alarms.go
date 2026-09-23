package eda

import (
	"fmt"
	"time"
)

// The alarm log holds the twenty most recent events, seven registers each from
// holding register 385: the alarm's code, its state, and when it was raised.
// The oldest falls off the end as new ones arrive.
const (
	AlarmLogCount  = 20
	AlarmLogBase   = 385
	AlarmLogStride = 7
)

// AlarmState is what an entry in the log is doing now.
type AlarmState int

// The three states an alarm can be in. A class A alarm stops the unit and will
// not clear until it has been acknowledged on the panel; a class B alarm
// reports without stopping anything.
const (
	AlarmOff          AlarmState = 0
	AlarmAcknowledged AlarmState = 1
	AlarmOn           AlarmState = 2
)

// AlarmClass is the severity the manual assigns.
type AlarmClass string

const (
	// ClassA stops the unit and needs acknowledging on the panel before it
	// will start again.
	ClassA AlarmClass = "A"
	// ClassB reports without stopping the unit.
	ClassB AlarmClass = "B"
	// ClassUnknown is for codes the manual's alarm table does not name.
	ClassUnknown AlarmClass = ""
)

// NameID and HelpID are where this alarm's words live in the message
// catalogue. This package holds no sentences: a sentence has a language, and
// which one depends on who is reading.
func (a Alarm) NameID() string { return fmt.Sprintf("alarm.%d.name", a.Code) }

// HelpID is the cause and the remedy, where the manual gives them. Not every
// code has one, so a caller checks before using it.
func (a Alarm) HelpID() string { return fmt.Sprintf("alarm.%d.help", a.Code) }

// StateID is the catalogue id for off, acknowledged or on.
func (s AlarmState) StateID() string {
	switch s {
	case AlarmOn:
		return "alarm.state.on"
	case AlarmAcknowledged:
		return "alarm.state.acknowledged"
	default:
		return "alarm.state.off"
	}
}

// alarmClasses maps each code in the log to the severity the manual assigns.
//
// The names and the remedies are not here: they are sentences people read, and
// people read in more than one language, so they live in the message catalogue
// under alarm.<code>.name and alarm.<code>.help. The classes stay because a
// class is a fact about the unit rather than a thing to say.
//
// From the unit manual's own alarm table (Alarmlista, in the installer
// chapter), with the numbering from the MD register list. Where the manual
// names no class, none is claimed: inventing a severity would be worse than
// admitting we do not know one.
var alarmClasses = map[int]AlarmClass{
	1:  ClassB,
	2:  ClassB,
	3:  ClassA,
	4:  ClassA,
	5:  ClassB,
	6:  ClassA,
	7:  ClassUnknown,
	8:  ClassA,
	9:  ClassA,
	10: ClassUnknown,
	11: ClassB,
	12: ClassA,
	13: ClassA,
	14: ClassB,
	15: ClassUnknown,
	16: ClassB,
	17: ClassB,
	18: ClassUnknown,
	19: ClassUnknown,
	20: ClassUnknown,
	21: ClassUnknown,
	22: ClassUnknown,
	24: ClassUnknown,
	25: ClassUnknown,
	26: ClassUnknown,
	27: ClassUnknown,
	28: ClassUnknown,
}

// ClassOf returns the severity for a code, or none for one the manual's table
// does not cover. An alarm nobody can name is still an alarm that happened, so
// an unknown code is reported rather than dropped.
func ClassOf(code int) AlarmClass { return alarmClasses[code] }

// Alarm is one entry in the unit's log.
type Alarm struct {
	// Index is 0 for the newest.
	Index int        `json:"index"`
	Code  int        `json:"code"`
	Class AlarmClass `json:"class"`
	State AlarmState `json:"state"`
	// Time is when the unit raised it, by the unit's own clock. That clock has
	// to be set by hand on the panel, so an old entry may carry a time that
	// was wrong when it was written.
	Time time.Time `json:"time"`
	// Empty marks a slot that has never held an alarm.
	Empty bool `json:"empty"`
}

// Active reports whether the alarm is on now.
func (a Alarm) Active() bool { return a.State == AlarmOn }

// ReadAlarms pulls the log out of a snapshot, newest first.
func ReadAlarms(holding []uint16) ([]Alarm, error) {
	last := AlarmLogBase + AlarmLogCount*AlarmLogStride
	if len(holding) < last {
		return nil, fmt.Errorf("eda: snapshot reaches %d, the alarm log ends at %d", len(holding), last)
	}

	out := make([]Alarm, 0, AlarmLogCount)
	for i := 0; i < AlarmLogCount; i++ {
		b := AlarmLogBase + i*AlarmLogStride
		code := int(holding[b])
		state := AlarmState(holding[b+1])
		year, month, day := 2000+int(holding[b+2]), int(holding[b+3]), int(holding[b+4])
		hour, minute := int(holding[b+5]), int(holding[b+6])

		a := Alarm{Index: i, Code: code, State: state}
		if code == 0 && month == 0 {
			a.Empty = true
			out = append(out, a)
			continue
		}
		a.Class = ClassOf(code)
		if month >= 1 && month <= 12 && day >= 1 && day <= 31 && hour <= 23 && minute <= 59 {
			a.Time = time.Date(year, time.Month(month), day, hour, minute, 0, 0, time.Local)
		}
		out = append(out, a)
	}
	return out, nil
}

// ActiveAlarms returns only the ones currently on.
func ActiveAlarms(all []Alarm) []Alarm {
	var out []Alarm
	for _, a := range all {
		if !a.Empty && a.Active() {
			out = append(out, a)
		}
	}
	return out
}
