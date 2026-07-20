# R0: agent harness trust boundaries

Source checklist: Luke Wren, "Stop Using OpenCode."
These labs do not assume tomo shares OpenCode's implementation.
They turn each relevant failure mode into a property that can be rerun against tomo.

Each lab must have teeth.
It first demonstrates that the adversarial input really entered the tested boundary, then asserts that the protected effect did not run.
