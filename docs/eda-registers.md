# What we have worked out about the EDA board

Enervent's own register list is the authoritative document and is not reproduced
here. This records what it does not say plainly, or says in a way that is easy
to read wrongly — established by measurement on the one unit named in the
README's status line, and on no other.

Holding registers throughout, Modbus RTU at 19200 8N1, unit 1. Register numbers
are the list's `3x00NN` without the prefix.

## What the unit is doing to the air

Two registers answer two different questions, and reading either alone gets it
wrong.

**HR49 — how much.** The list calls it "Cascade I", the integral term of the
cascade temperature controller, which sounds like an internal and is not: in a
PI controller the integral term *is* the control output. It is a ladder:

| HR49 | meaning |
|---|---|
| below 0 | cooling, by that many per cent |
| 0 to 99 | heat recovery, at that per cent |
| 100 and above | recovery at full, plus heating by the remainder |

**HR45 — which.** "Control steps of temperature", an enumeration:

| HR45 | |  | HR45 | |
|---|---|---|---|---|
| 0 | nothing | | 6 | summer night cooling |
| 1 | cooling | | 7 | starting up |
| 2 | heat recovery | | 8 | stopped |
| 4 | heating | | 9 | heat recovery cleaning |
| 5 | waiting to change step | | 10 | external unit defrost |

3, and anything above 10, the list does not name.

The two agree, and watching them agree is what settled the ladder. In two
consecutive readings nine seconds apart:

```
HR45=2  HR49=101      recovery
HR45=4  HR49=102      heating
```

HR49 crossed 100 and HR45 changed from recovery to heating in the same reading.
The threshold is real and it is at 100.

HR45 carries what a proportion cannot express — starting up, stopped,
defrosting — so an interface wanting both needs both.

Related: **HR165/166/167 (LTO25, LTO50, LTO75)** are the voltage scaling points
for the heat recovery at 25, 50 and 75 per cent, which is independent
confirmation that the exchanger is driven as a 0–100 % signal.

## Why the unit is doing it

The unit does not heat the room. An outer loop compares the room with the
setpoint and computes a target for the supply air; an inner loop hits that
target with the exchanger, the heater or the cooler.

| | |
|---|---|
| **HR47** | Cascade SP — the supply air temperature it has decided on |
| **HR138** | SPLY T MIN — the lowest it is allowed to ask for |
| **HR139** | SPLY T MAX — the highest |

Measured with the room about a degree above the setpoint and the outdoor air
some ten degrees colder, HR47 sat at exactly the value of HR138. The controller
had asked for everything it was allowed and was still not satisfied, so the
exchanger was ramped to zero and cold outdoor air let straight in.

That is the answer to "why is the supply so cold when I asked for 22", and without
HR47 the page has none.

It also settles a related question: the unit does **not** run the exchanger
backwards to recover cold. When cooling is wanted it bypasses it, and cooling
proper is the negative half of the HR49 ladder — the heat pump.

## The clock

**HR582–586 are the setting block**: minute, hour, day, month, year. Writing
them sets the clock.

**HR37–43 are the running clock** — second, minute, hour, day, month, year,
weekday. They are readable, they acknowledge a write, and the unit overwrites
them a second later. An interface that writes there sees a successful write and
no effect.

The setting block has no seconds field and zeroes the seconds, so a write has to
be timed to a minute boundary or it lands up to a minute out. Doing that gets
the drift to about two tenths of a second.

Drift has to be measured against the instant the registers were read, not
against the present: a snapshot is up to a poll interval old, and using the
present adds that whole interval.

There is no daylight-saving adjustment anywhere in the board, so an hour of
drift twice a year is the unit being correct about a thing nobody told it.

## Sensors worth knowing about

| | |
|---|---|
| **HR1** | T_OP1, the thermometer in the control panel |
| **HR46** | MEAN_TEMP, the mean room temperature the unit controls against |
| **HR35** | RH_MEAN, mean humidity |
| **HR27** | AI5_RES, CO2 — zero on a unit without the sensor, which is most |
| **HR29/30** | heat recovery efficiency, supply and extract |

On a unit whose only room sensor is the panel, HR1 and HR46 read the same.

## Machine identity

**HR597** is the machine family and **HR598** the serial number. Enervent's own
name table maps the family code to a model name. The serial is zero on a unit
that never had one written, which is not rare.

## Service

**HR710** is days since the last service and is writable — writing zero resets
the countdown, which is what the panel's own reset does.

## Documented ranges the unit does not enforce

The register list gives 0–60 minutes for overpressure (**HR57**) and manual
boost (**HR66**), and a range for the timer setback (**HR172**). The board
accepts values outside all of them: 255 in HR172 and 90 in HR56/57/66 were
written and read back unchanged.

So a limit in an interface is a limit the interface has chosen. That is a
reasonable thing to do — a two-hour overpressure is probably a typo — but it
should be described as a decision rather than as what the unit allows.

## Function code 6

Write-single-register works and reaches the board directly. Failures seen
through the original Freeway WEB adapter were the adapter's, not the board's.
