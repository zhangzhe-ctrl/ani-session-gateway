# Realtime Session Gateway

This context covers short-lived browser sessions for exec, serial, and VNC interaction with ANI workloads.

## Language

**Connected Session**:
A Session whose one-time ticket has been claimed and whose browser connection has been accepted. It is establishing until its runtime stream opens, active while carrying an exec, serial, or VNC interaction, and ends when either endpoint closes or a session limit is reached.
_Avoid_: Active Session, Open Session
