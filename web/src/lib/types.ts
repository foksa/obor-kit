// The API's JSON, as internal/web and internal/view write it (Go's json tags; omitempty fields optional).

export interface Background {
  pid: number;
  command: string;
  background: boolean;
  label?: string;
  age: number;
}

export interface Running {
  step: string;
  task?: string;
  command?: string;
  since: number;
}

export interface Serve {
  up: boolean;
  since?: string;
  error?: string;
  herdr: boolean;
  herdr_note?: string;
  skew?: string;
  project: boolean;
  merging: boolean;
  log_error?: string;
  pid?: number;
  launchd?: string;
}

export interface Job {
  id: string;
  delivered: boolean;
}

// Task is one task the view knows: the sections name it by id, project.tasks[id] (taskOf) has its fields.
export interface Task {
  task: string;
  title?: string;
  spec_rev?: number;
  lane?: string;
  agent?: string;
  task_state?: string;
  open?: boolean;
  phase?: string;
  step?: number;
  attempt?: number;
  running?: string;
  effort?: string;
  ref?: string; // its spec's Ref: an external tracker's id
  job?: Job;
}

export interface Lane {
  lane: string;
  type: string;
  dir: string;
  branch: string | null;
  task: string | null;
  state: string;
  reason: string | null;
  done_age: number | null;
  dirty: boolean;
  warning: string | null;
  op?: string;
  mode?: string; // "keep": a keep lane, its agent kept between tasks
  agent?: string;
  agent_status?: string;
  free: boolean;
  column: string;
  since?: number;
  background?: Background[];
  queue?: string[]; // a keep lane's queue, in serve's pass order
}

export interface BriefStatus {
  to: string;
  since: number;
  delivered: boolean;
  worked?: boolean;
}

export interface Role {
  role: string;
  agent: string;
  status?: string;
  age?: number;
  brief?: BriefStatus;
  session?: string;
  background?: Background[];
}

export interface NeedsYou {
  task: string;
  reason: string;
  answer?: string;
  since?: number;
}

export interface FullCheck {
  sha: string;
  verdict: string;
  at: number;
  summary: string;
}

export interface Pause {
  until: number | null;
  reason: string;
  by?: string;
}

export interface PendingStep {
  step: string;
  task?: string;
  since: number;
  error?: string;
  failed_at?: number;
}

export interface TaskStatus {
  task: string;
  lane: string;
  attempt: number;
  state: string;
  ended?: boolean;
  phase: string;
  stage?: string;
  running?: string;
  after?: string[];
  why?: string;
  high?: boolean; // its dispatch goes first for the next free lane when it waits
  waiting?: boolean; // the merge loop's waiting predicate: the view's queue lists it
  rank?: number; // waiting: its place in serve's pass order, the queue's order (absent: not a waiting dispatch)
  level?: string;
  types?: string[];
  difficulty?: string;
  model?: string;
  ref?: string; // its spec's Ref: an external tracker's id
  effort?: string;
  job?: string;
  delivered?: boolean;
  stopping?: string;
  started?: number; // when serve last tried to start its lane agent (epoch seconds)
}

export interface AgentStatus {
  machine: string;
  workspace: string;
  pane: string;
  name?: string;
  agent: string;
  lane?: string;
  task?: string;
  status: string;
  age: number;
  session?: string;
  transcript?: string;
  started?: number;
}

export interface MessageStatus {
  to: string;
  from?: string;
  line: string;
  age: number;
  held?: string;
}

export interface WatchStatus {
  task: string;
  by?: string;
  note?: string;
  since: number;
}

// Idle is the project's idle state: the pipeline is empty since its fact's time, what ended it, and the
// lead's acknowledgement (acked and why).
export interface Idle {
  since: number;
  last?: string;
  acked?: boolean;
  why?: string;
}

export interface MergeItem {
  task: string;
  stage: string;
  waits_on?: string;
  why?: string;
}

export interface WaitItem {
  task: string;
  waits_on: string;
  after?: string[];
  why?: string;
  high?: boolean; // its dispatch goes first for the next free lane (Priority: high)
  level?: string;
  types?: string[];
  queue?: string; // the keep lane it queues on
  difficulty?: string;
  model?: string;
  since?: number;
}

export interface ReviewItem {
  after?: string[];
  waiting?: string[];
  held?: string;
  note?: string;
  by?: string;
  since: number;
  state: string;
}

export interface OutboxItem {
  kind: string;
  to: string;
  from?: string;
  line?: string;
  since: number;
  delivered?: boolean;
  worked?: boolean;
  held?: string;
}

export interface Queue {
  scratch: { task: string; why: string } | null;
  merge: MergeItem[];
  waiting: WaitItem[];
  review: ReviewItem[];
  outbox: OutboxItem[];
  pause: Pause | null;
  nightly: { at: string; next: number } | null;
}

export interface Event {
  seq: number;
  ts: string;
  task?: string;
  line: string;
  bad?: boolean;
  kind: string;
}

export interface EventsPage {
  seq: number;
  events: Event[];
}

export interface ProjectView {
  serve: Serve;
  running: Running[];
  pending: PendingStep[];
  lanes: Lane[];
  tasks: Record<string, Task>;
  agents: AgentStatus[];
  roles: Role[];
  lead?: Role;
  needs_you: NeedsYou[];
  full_check: FullCheck | null;
  paused: Pause | null;
  messages: MessageStatus[];
  review?: string;
  watches?: WatchStatus[];
  idle?: Idle;
  queue: Queue;
  events: Event[];
  notes?: string[];
}

// LaneDot is one lane of /api/projects: enough for the fleet page's lane strip; its task's fields are Task's (a free
// lane's: its last task's id alone).
export interface LaneDot {
  lane: string;
  column: string;
  type?: string;
  free: boolean;
  since?: number;
  task: string;
  title?: string;
  spec_rev?: number;
  agent?: string;
  task_state?: string;
  open?: boolean;
  phase?: string;
  step?: number;
  attempt?: number;
  running?: string;
  effort?: string;
  ref?: string; // its spec's Ref: an external tracker's id
  job?: Job;
}

export interface ProjectsEntry {
  name: string;
  root: string;
  up: boolean;
  busy: number;
  free: number;
  needs_you: number;
  queue: number;
  running: Running[];
  roles: Role[];
  background: number;
  lanes: LaneDot[];
  full_check?: FullCheck | null;
  paused?: Pause | null;
  events: Event[];
  error?: string;
}

export interface TaskRecord {
  seq: number;
  ts: string;
  step: string;
  kind: string;
  line?: string;
  outcome?: string; // a gate's: green, red, conflict, fail
  pass?: boolean; // a gate's pass, the outcome as a yes or no
}

export interface Prompt {
  seq: number;
  kind: string;
  job?: string;
  task?: string;
  agent: string;
  ts: string;
  text: string;
}

// TaskDetail is a task's page: its Task, then what the page adds.
export interface TaskDetail {
  task: string;
  title?: string;
  spec_rev?: number;
  lane?: string;
  agent?: string;
  task_state?: string;
  open?: boolean;
  phase?: string;
  step?: number;
  attempt?: number;
  running?: string;
  effort?: string;
  ref?: string; // its spec's Ref: an external tracker's id
  job?: Job;
  dispatched_at?: number;
  spec?: string;
  gated_rev?: number;
  report?: string;
  gate_line?: string;
  gate_pass?: boolean; // the last gate's pass, from serve (never the line's wording)
  needs_you: NeedsYou[];
  records: TaskRecord[];
  jobs: Prompt[];
}

export interface AgentSession {
  agent: string;
  id: string;
  kind?: string;
  transcript?: string;
  last_write?: number;
  pane?: string;
  lane?: string;
  task?: string;
  started: number;
  source?: string;
}

export interface AgentLine {
  seq: number;
  ts: string;
  from?: string;
  line: string;
}

export interface AgentDetail {
  agent: string;
  status?: string;
  lane?: string;
  task?: string;
  sessions: AgentSession[];
  briefs: Prompt[];
  lines: AgentLine[];
  jobs: Prompt[];
  background?: Background[];
}

export interface SetupLevel {
  level: string;
  types: string[];
}

// SetupType is a model ref the setup names and what runs it.
export interface SetupType {
  ref: string;
  name: string;
  kind: string;
  model: string;
  effort: string;
}

// SetupModel is a model the config knows: built-in or models' own, its kind's efforts and its pause.
export interface SetupModel {
  name: string;
  kind: string;
  model: string;
  builtin: boolean;
  efforts: string[];
  paused: Pause | null;
}

// SetupKind is an agent kind: the args its starts get and whether it has a job_note.
export interface SetupKind {
  name: string;
  args: string[];
  job_note: boolean;
}

export interface SetupLane {
  lane: string;
  type: string;
  agent: string;
  state: string;
  locked?: boolean;
}

export interface SetupRole {
  role: string;
  agent: string;
  type: string;
  status: string;
}

export interface DispatchCount {
  level: string;
  type: string;
  recent: number;
  all: number;
}

export interface Setup {
  difficulty: SetupLevel[];
  types: SetupType[];
  models: SetupModel[];
  kinds: SetupKind[];
  lanes: SetupLane[];
  roles: SetupRole[];
  paused: Pause | null;
  distribution: DispatchCount[];
  config_rev: string;
}

// /api/help (internal/help): a command's row, a README section, and the page's whole answer.
export interface HelpCommand {
  name: string;
  usage: string;
  desc: string;
  group: string;
}

export interface HelpSection {
  title: string;
  body: string;
}

export interface Help {
  groups: string[];
  commands: HelpCommand[];
  sections: HelpSection[];
}
