# Questions for the lead

- An answer's line retires when its task's worker takes its job (`delivered`: its `job_next`, the question of a failed
  delivery settled). `docs/design.md` ("A queued line is in one state": "An answer's line retires once its question is
  settled another way — the worker submits or asks again, …") should list that trigger too; the file is lead-owned.
