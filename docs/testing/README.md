# Testing

Testing is layered:

```text
unit -> integration -> Docker actors -> real three-machine gate
```

Unit and integration tests provide determinism and negative coverage. Docker validates process/config isolation. The three-machine gate validates real relay/NAT and distributed lifecycle behavior.

Canonical behavior matrix: [`ACCEPTANCE_MATRIX.md`](ACCEPTANCE_MATRIX.md).
