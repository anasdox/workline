"""
LangChain + Workline SDK example.

Prereqs:
  - Start Workline API: wl serve --addr 127.0.0.1:8080 --base-path /v0
  - Install deps: pip install langchain langchain-openai
  - Set OpenAI key: export OPENAI_API_KEY=...

Optional env vars:
  - WORKLINE_BASE_URL (default: http://127.0.0.1:8080)
  - WORKLINE_PROJECT_ID (default: myproj)
  - WORKLINE_API_KEY (API key for automation)
  - WORKLINE_PLANNER_API_KEY (API key for planner role)
  - WORKLINE_EXECUTOR_API_KEY (API key for executor role)
  - WORKLINE_REVIEWER_API_KEY (API key for reviewer role)
  - WORKLINE_ACCESS_TOKEN (JWT bearer token)
  - WORKLINE_PLANNER_ACCESS_TOKEN (JWT for planner role)
  - WORKLINE_EXECUTOR_ACCESS_TOKEN (JWT for executor role)
  - WORKLINE_REVIEWER_ACCESS_TOKEN (JWT for reviewer role)
  - WORKLINE_HUMAN_REVIEW_MODE (interactive only)

This script:
  1) Runs discovery with a product planner that can ask humans questions.
  2) Ensures a "problem refinement" task exists and stores discovery output in it.
  3) Produces an agile iteration plan (sprint goal, backlog, dependencies).
  4) Runs a human-in-the-loop review step (interactive).
  5) Generates a PRD and Gherkin specs for all features.
  6) Creates a Workline iteration, then tasks per sprint backlog item.
  7) Runs demo/review gates, closes the iteration, then fetches events.
  8) Logs all agent actions into a Workline "agent log" task.
"""

import json
import os
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Dict, List, Optional

try:
    from langchain.agents import create_agent as lc_create_agent

    _LC_AGENT_MODE = "graph"
except ImportError:
    lc_create_agent = None
    _LC_AGENT_MODE = "executor"

if _LC_AGENT_MODE == "executor":
    try:
        from langchain.agents import AgentExecutor, create_tool_calling_agent
    except ImportError:
        from langchain.agents import create_tool_calling_agent
        from langchain.agents.agent import AgentExecutor
from langchain_core.prompts import ChatPromptTemplate
from langchain_core.tools import tool
from langchain_openai import ChatOpenAI

_ROOT = Path(__file__).resolve().parents[1]
_SDK_PATH = _ROOT / "sdk" / "python"
if str(_SDK_PATH) not in sys.path:
    sys.path.insert(0, str(_SDK_PATH))

from workline import APIError, WorklineClient

BASE_URL = os.getenv("WORKLINE_BASE_URL", "http://127.0.0.1:8080")
PROJECT_ID = os.getenv("WORKLINE_PROJECT_ID", "example")
API_KEY = os.getenv("WORKLINE_API_KEY")
ACCESS_TOKEN = os.getenv("WORKLINE_ACCESS_TOKEN")
HUMAN_REVIEW_MODE = os.getenv("WORKLINE_HUMAN_REVIEW_MODE", "interactive")

PLANNER_API_KEY = os.getenv("WORKLINE_PLANNER_API_KEY")
EXECUTOR_API_KEY = os.getenv("WORKLINE_EXECUTOR_API_KEY")
REVIEWER_API_KEY = os.getenv("WORKLINE_REVIEWER_API_KEY")
PLANNER_ACCESS_TOKEN = os.getenv("WORKLINE_PLANNER_ACCESS_TOKEN")
EXECUTOR_ACCESS_TOKEN = os.getenv("WORKLINE_EXECUTOR_ACCESS_TOKEN")
REVIEWER_ACCESS_TOKEN = os.getenv("WORKLINE_REVIEWER_ACCESS_TOKEN")


planner_client = WorklineClient(
    BASE_URL,
    PROJECT_ID,
    api_key=PLANNER_API_KEY or API_KEY,
    access_token=PLANNER_ACCESS_TOKEN or ACCESS_TOKEN,
)
executor_client = WorklineClient(
    BASE_URL,
    PROJECT_ID,
    api_key=EXECUTOR_API_KEY or API_KEY,
    access_token=EXECUTOR_ACCESS_TOKEN or ACCESS_TOKEN,
)
reviewer_client = WorklineClient(
    BASE_URL,
    PROJECT_ID,
    api_key=REVIEWER_API_KEY or API_KEY,
    access_token=REVIEWER_ACCESS_TOKEN or ACCESS_TOKEN,
)
client = planner_client
CURRENT_WORKSHOP_ID: Optional[str] = None


def _client_for_attestation(kind: str) -> WorklineClient:
    normalized = kind.strip().lower()
    if normalized.startswith("review.") or normalized.startswith("acceptance.") or normalized.startswith("security.") or normalized == "iteration.approved":
        return reviewer_client
    if normalized.startswith("ci."):
        return executor_client
    if normalized.startswith("workshop."):
        return planner_client
    return client


@tool
def add_workline_attestation(entity_kind: str, entity_id: str, kind: str) -> Dict[str, str]:
    """Add an attestation to a Workline entity (task, iteration, etc)."""
    attestation = _client_for_attestation(kind).add_attestation(entity_kind, entity_id, kind)
    return {
        "id": attestation.id,
        "entity_kind": attestation.entity_kind,
        "entity_id": attestation.entity_id,
        "kind": attestation.kind,
        "actor_id": attestation.actor_id,
    }


@tool
def create_workline_task_full(
    title: str,
    task_type: str = "feature",
    iteration_id: Optional[str] = None,
    assignee_id: Optional[str] = None,
    depends_on: Optional[List[str]] = None,
    description: Optional[str] = None,
    priority: Optional[int] = None,
) -> Dict[str, str]:
    """Create a Workline task with iteration, dependencies, and owner."""
    body: Dict[str, object] = {"title": title, "type": task_type}
    if iteration_id:
        body["iteration_id"] = iteration_id
    if assignee_id:
        body["assignee_id"] = assignee_id
    if depends_on:
        body["depends_on"] = depends_on
    if description:
        body["description"] = description
    if priority is not None:
        body["priority"] = priority
    data = planner_client._request("POST", planner_client._project_path("tasks"), body)
    return {
        "id": data["id"],
        "title": data["title"],
        "type": data["type"],
        "status": data["status"],
        "iteration_id": data.get("iteration_id"),
        "assignee_id": data.get("assignee_id"),
        "priority": data.get("priority"),
    }


@tool
def create_workline_iteration(iteration_id: str, goal: str) -> Dict[str, str]:
    """Create a Workline iteration."""
    body = {"id": iteration_id, "goal": goal}
    data = planner_client._request("POST", planner_client._project_path("iterations"), body)
    return {"id": data["id"], "goal": data["goal"], "status": data["status"]}


@tool
def set_workline_iteration_status(iteration_id: str, status: str, force: bool = False) -> Dict[str, str]:
    """Update Workline iteration status (pending -> running -> delivered -> validated)."""
    body = {"status": status}
    url = executor_client._project_path(f"iterations/{iteration_id}/status")
    if force:
        url = f"{url}?force=true"
    data = executor_client._request("PATCH", url, body)
    return {"id": data["id"], "status": data["status"]}


@tool
def latest_workline_events(limit: int = 5) -> List[Dict[str, str]]:
    """Fetch the latest Workline events for audit/debugging."""
    events = planner_client.events(limit)
    return [
        {
            "id": str(event.id),
            "type": event.type,
            "entity_kind": event.entity_kind,
            "entity_id": event.entity_id,
            "actor_id": event.actor_id,
        }
        for event in events
    ]


@tool
def list_workline_iterations(limit: int = 50) -> List[Dict[str, str]]:
    """List recent iterations."""
    data = planner_client._request("GET", planner_client._project_path(f"iterations?limit={limit}"))
    items = data.get("items", data)
    return [
        {"id": item["id"], "goal": item["goal"], "status": item["status"]}
        for item in items
    ]


@tool
def list_workline_tasks(iteration_id: Optional[str] = None, status: Optional[str] = None, limit: int = 50) -> List[Dict[str, str]]:
    """List tasks, optionally filtered by iteration or status."""
    params = []
    if iteration_id:
        params.append(f"iteration_id={iteration_id}")
    if status:
        params.append(f"status={status}")
    params.append(f"limit={limit}")
    query = "&".join(params)
    data = planner_client._request("GET", planner_client._project_path(f"tasks?{query}"))
    items = data.get("items", data)
    return [
        {
            "id": item["id"],
            "title": item["title"],
            "status": item["status"],
            "iteration_id": item.get("iteration_id"),
            "priority": item.get("priority"),
        }
        for item in items
    ]


@tool
def update_workline_task_priority(task_id: str, priority: int) -> Dict[str, object]:
    """Set priority for a Workline task."""
    body = {"priority": priority}
    data = planner_client._request("PATCH", planner_client._project_path(f"tasks/{task_id}"), body)
    return {"id": data["id"], "priority": data.get("priority"), "status": data.get("status")}


def _get_task(task_id: str) -> Optional[Dict[str, object]]:
    try:
        return client._request("GET", client._project_path(f"tasks/{task_id}"))
    except APIError as err:
        if err.status_code == 404:
            return None
        raise


def _create_problem_refinement_task(task_id: str) -> Dict[str, object]:
    body = {
        "id": task_id,
        "title": "Problem refinement",
        "type": "workshop",
        "description": "Capture the refined problem statement and assumptions.",
        "policy": {"preset": "workshop.problem_refinement"},
    }
    return client._request("POST", client._project_path("tasks"), body)


def ensure_problem_refinement_task() -> str:
    task = _ensure_problem_refinement_task_exists()
    return task["id"]


def _ensure_problem_refinement_task_exists() -> Dict[str, object]:
    task_id = "problem-refinement"
    task = _get_task(task_id)
    if task is None:
        task = _create_problem_refinement_task(task_id)
    return task


def ensure_workshop_task(task_id: str, title: str, preset: str, description: str) -> Dict[str, object]:
    task = _get_task(task_id)
    if task is None:
        body = {
            "id": task_id,
            "title": title,
            "type": "workshop",
            "description": description,
            "policy": {"preset": preset},
        }
        task = client._request("POST", client._project_path("tasks"), body)
    return task


def set_current_workshop(task_id: Optional[str]) -> None:
    global CURRENT_WORKSHOP_ID
    CURRENT_WORKSHOP_ID = task_id


def append_workshop_conversation(task_id: str, question: str, answer: str) -> None:
    entry = {
        "ts": datetime.now(timezone.utc).isoformat(),
        "question": question,
        "answer": answer,
    }
    client.append_work_outcomes(task_id, "conversation", entry)


def get_workshop_output(task_id: str) -> Optional[str]:
    task = _get_task(task_id)
    if not task:
        return None
    outcomes = task.get("work_outcomes") or {}
    output = outcomes.get("output")
    if isinstance(output, str) and output.strip():
        return output
    return None


def set_workshop_output(task_id: str, output: str) -> None:
    client.put_work_outcomes(task_id, "output", output)
    client.put_work_outcomes(task_id, "summary", output)
    append_workshop_conversation(task_id, "Workshop summary", output)


def get_problem_statement_from_workline() -> Optional[str]:
    task = _get_task("problem-refinement")
    if not task:
        return None
    work_outcomes = task.get("work_outcomes") or {}
    statement = work_outcomes.get("problem_statement")
    if isinstance(statement, str) and statement.strip():
        return statement
    return None


def set_problem_statement_in_workline(problem_statement: str) -> None:
    task = _ensure_problem_refinement_task_exists()
    client.put_work_outcomes(task["id"], "problem_statement", problem_statement)


def log_conversation(question: str, answer: str) -> None:
    if CURRENT_WORKSHOP_ID is None:
        ensure_problem_refinement_task()
    task_id = CURRENT_WORKSHOP_ID or "problem-refinement"
    append_workshop_conversation(task_id, question, answer)


def get_problem_refinement_discovery() -> Optional[Dict[str, object]]:
    outputs = {
        "initial": get_workshop_output("problem-refinement"),
        "event_storming": get_workshop_output("workshop-eventstorming"),
        "decision_workshop": get_workshop_output("workshop-decision"),
        "clarify": get_workshop_output("workshop-clarify"),
    }
    if not any(outputs.values()):
        return None
    return {k: v for k, v in outputs.items() if v}


def ask_human_question_local(question: str, options: Optional[List[str]] = None) -> str:
    if HUMAN_REVIEW_MODE == "interactive":
        if not sys.stdin.isatty():
            raise RuntimeError("stdin is not interactive; run without make or attach a TTY.")
        print("\n=== Question ===\n", flush=True)
        print(question, flush=True)
        if options:
            for idx, opt in enumerate(options, start=1):
                print(f"{idx}. {opt}", flush=True)
        while True:
            print("Your answer: ", end="", flush=True)
            answer = sys.stdin.readline()
            if answer == "":
                raise RuntimeError("stdin closed while waiting for input")
            answer = answer.strip()
            if answer:
                if options and answer.isdigit():
                    choice = int(answer)
                    if 1 <= choice <= len(options):
                        selected = options[choice - 1]
                        if selected.lower().startswith("other"):
                            detail = input("Please specify: ").strip()
                            if detail:
                                log_conversation(question, detail)
                                return detail
                        log_conversation(question, selected)
                        return selected
                log_conversation(question, answer)
                return answer
            print("Please enter a non-empty answer.", flush=True)
    raise RuntimeError("HUMAN_REVIEW_MODE must be interactive; simulation is disabled.")


@tool
def ask_human_question(question: str, options: Optional[List[str]] = None) -> str:
    """Ask a human for a decision or clarification."""
    return ask_human_question_local(question, options)


def ask_human_for_review(draft: str) -> str:
    if HUMAN_REVIEW_MODE == "interactive":
        if not sys.stdin.isatty():
            raise RuntimeError("stdin is not interactive; run without make or attach a TTY.")
        print("\n=== Draft Plan (for review) ===\n", flush=True)
        print(draft, flush=True)
        print("\n=== Provide edits or approvals ===\n", flush=True)
        print("1. approve", flush=True)
        print("2. request changes", flush=True)
        while True:
            print("Enter review feedback (or 'approve'): ", end="", flush=True)
            feedback = sys.stdin.readline()
            if feedback == "":
                raise RuntimeError("stdin closed while waiting for input")
            feedback = feedback.strip()
            if feedback:
                if feedback.isdigit():
                    if feedback == "1":
                        return "approve"
                    if feedback == "2":
                        return "request changes"
                return feedback
            print("Please enter a response (or type 'approve').", flush=True)
    raise RuntimeError("HUMAN_REVIEW_MODE must be interactive; simulation is disabled.")




def _run_agent(model: ChatOpenAI, system_prompt: str, user_input: str, tools: List) -> str:
    if not tools:
        response = model.invoke(
            [
                {"role": "system", "content": system_prompt},
                {"role": "user", "content": user_input},
            ]
        )
        if hasattr(response, "content"):
            return response.content
        return str(response)
    if _LC_AGENT_MODE == "graph" and lc_create_agent is not None and tools:
        try:
            agent = lc_create_agent(
                model,
                tools=tools,
                system_prompt=system_prompt,
                debug=True,
                interrupt_after=["tools"],
            )
            result = agent.invoke({"messages": [{"role": "user", "content": user_input}]})
            messages = result.get("messages", [])
            for msg in reversed(messages):
                if hasattr(msg, "content"):
                    return msg.content
                if isinstance(msg, dict) and msg.get("content"):
                    return msg["content"]
            return ""
        except ValueError:
            pass

    try:
        create_agent_fn = create_tool_calling_agent
    except NameError:
        from langchain.agents import create_tool_calling_agent as create_agent_fn
        from langchain.agents.agent import AgentExecutor as Executor
    else:
        Executor = AgentExecutor
    prompt = ChatPromptTemplate.from_messages(
        [
            ("system", system_prompt),
            ("human", user_input),
            ("placeholder", "{agent_scratchpad}"),
        ]
    )
    agent = create_agent_fn(model, tools, prompt)
    executor = Executor(agent=agent, tools=tools, verbose=True, max_iterations=6)
    return executor.invoke({"input": ""})["output"]


def _extract_json_payload(text: str) -> Optional[object]:
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass
    match = re.search(r"(\{.*\}|\[.*\])", text, re.DOTALL)
    if not match:
        return None
    try:
        return json.loads(match.group(1))
    except json.JSONDecodeError:
        return None


def run_discovery(model: ChatOpenAI, problem_statement: str) -> str:
    system_prompt = (
        "You are a product planner starting a discovery phase. First, refine "
        "the provided problem statement into clear goals and scope. Only ask "
        "clarifying questions if critical details are missing. Use "
        "ask_human_question when needed, and always include 3-5 options plus "
        "'Other'. Ask at most one question at a time. "
        "'Other' in each question. Then provide assumptions and success metrics. "
        "Output sections: Refined Problem, Assumptions, Success Metrics, Actors."
    )
    tools = [ask_human_question]
    return _run_agent(model, system_prompt, problem_statement, tools)


def run_discovery_phase(model: ChatOpenAI, phase_name: str, context: str) -> str:
    system_prompt = (
        f"You are facilitating a discovery workshop phase: {phase_name}. "
        "Ask clarifying questions when needed using ask_human_question, and "
        "include 3-5 options plus 'Other' in each question. "
        "Ask at most one question at a time. "
        "Return a concise summary with decisions, risks, and open questions."
    )
    tools = [ask_human_question]
    return _run_agent(model, system_prompt, context, tools)


def run_workshop_next_steps(model: ChatOpenAI, phase_name: str, context: str) -> str:
    system_prompt = (
        f"You just completed the {phase_name} workshop. "
        "Plan the next steps needed to reach the iteration goal. "
        "Return a concise, ordered list with owners if possible."
    )
    return _run_agent(model, system_prompt, context, tools=[])


def continue_discovery(
    model: ChatOpenAI,
    problem_statement: str,
    existing: Optional[Dict[str, object]],
) -> Dict[str, object]:
    discovery: Dict[str, object] = existing or {}
    ensure_workshop_task(
        "problem-refinement",
        "Problem refinement",
        "workshop.problem_refinement",
        "Refine the problem statement and clarify scope.",
    )
    ensure_workshop_task(
        "workshop-eventstorming",
        "Event storming",
        "workshop.eventstorming",
        "Map domain events and flows.",
    )
    ensure_workshop_task(
        "workshop-decision",
        "Decision workshop",
        "workshop.decision",
        "Capture key trade-offs and decisions.",
    )
    ensure_workshop_task(
        "workshop-clarify",
        "Clarification workshop",
        "workshop.clarify",
        "Resolve open questions and assumptions.",
    )
    if "initial" not in discovery or not str(discovery.get("initial", "")).strip():
        initial_output = get_workshop_output("problem-refinement")
        if not initial_output:
            set_current_workshop("problem-refinement")
            initial_output = run_discovery(model, problem_statement)
            set_current_workshop(None)
            set_workshop_output("problem-refinement", initial_output)
        discovery["initial"] = initial_output
    context = "\n\n".join(
        [
            f"Problem Statement:\n{problem_statement}",
            f"Initial Refinement:\n{discovery.get('initial','')}",
        ]
    )
    if "event_storming" not in discovery:
        event_out = get_workshop_output("workshop-eventstorming")
        if not event_out:
            set_current_workshop("workshop-eventstorming")
            event_out = run_discovery_phase(model, "Event Storming", context)
            set_current_workshop(None)
            set_workshop_output("workshop-eventstorming", event_out)
            next_steps = run_workshop_next_steps(model, "Event Storming", context)
            client.put_work_outcomes("workshop-eventstorming", "next_steps", next_steps)
        discovery["event_storming"] = event_out
    if "decision_workshop" not in discovery:
        decision_out = get_workshop_output("workshop-decision")
        if not decision_out:
            set_current_workshop("workshop-decision")
            decision_out = run_discovery_phase(model, "Decision Workshop", context)
            set_current_workshop(None)
            set_workshop_output("workshop-decision", decision_out)
            next_steps = run_workshop_next_steps(model, "Decision Workshop", context)
            client.put_work_outcomes("workshop-decision", "next_steps", next_steps)
        discovery["decision_workshop"] = decision_out
    if "clarify" not in discovery:
        clarify_out = get_workshop_output("workshop-clarify")
        if not clarify_out:
            set_current_workshop("workshop-clarify")
            clarify_out = run_discovery_phase(model, "Clarification", context)
            set_current_workshop(None)
            set_workshop_output("workshop-clarify", clarify_out)
            next_steps = run_workshop_next_steps(model, "Clarification", context)
            client.put_work_outcomes("workshop-clarify", "next_steps", next_steps)
        discovery["clarify"] = clarify_out
    return discovery


def run_planner(model: ChatOpenAI, discovery_output: str) -> str:
    system_prompt = (
        "You are a product owner running agile planning. Based on discovery, "
        "create a one-iteration plan with dependencies and owners. Workline "
        "iterations have no duration and we do not do capacity planning here. "
        "Assume the work is carried out by AI actors (e.g., PlannerAgent, "
        "OpsAgent, ReviewerAgent) and assign owners accordingly. Ask questions "
        "only when decisions are needed"
        "using ask_human_question, always providing 3-5 options plus 'Other'. "
        "Ask at most one question at a time. "
        "Output sections: Iteration ID, Sprint Goal, "
        "User Stories (with actors), Dependencies, Acceptance "
        "Criteria, Risks, Sprint Backlog (each item with owner and dependencies)."
    )
    tools = [ask_human_question]
    return _run_agent(model, system_prompt, discovery_output, tools)


def run_workline(model: ChatOpenAI, final_plan: str) -> str:
    system_prompt = (
        "You are a delivery lead. You will receive a finalized agile plan that "
        "includes an Iteration ID and Sprint Backlog. Do the following:\n"
        "1) Check if the iteration already exists using list_workline_iterations.\n"
        "2) If missing, create the iteration (status will be pending).\n"
        "3) Always list tasks for this iteration using list_workline_tasks. "
        "Use the existing task IDs to avoid duplicates and to wire dependencies.\n"
        "4) Only create missing backlog items with create_workline_task_full. "
        "Map dependency names to existing task IDs when possible.\n"
        "5) Prioritize all tasks before starting the iteration. Assign "
        "priority 1..N (1 is highest). If any existing task is missing a "
        "priority, set it with update_workline_task_priority.\n"
        "6) Move iteration to running if not already.\n"
        "7) Ask the human if demo/review is approved. If approved, add an "
        "iteration.approved attestation on the iteration.\n"
        "8) Move iteration to delivered, then validated.\n"
        "9) Add ci.passed and review.approved attestations for each task.\n"
        "10) Call latest_workline_events.\n"
        "Use ask_human_question for any decision points and provide options. "
        "Ask at most one question at a time. Output a short "
        "'Workline Actions' section listing created task IDs and iteration status."
    )
    tools = [
        create_workline_iteration,
        set_workline_iteration_status,
        create_workline_task_full,
        update_workline_task_priority,
        add_workline_attestation,
        latest_workline_events,
        list_workline_iterations,
        list_workline_tasks,
        ask_human_question,
    ]
    return _run_agent(model, system_prompt, final_plan, tools)


def _extract_iteration_id(plan: str) -> Optional[str]:
    match = re.search(r"iteration id\\s*[:\\-]\\s*([\\w-]+)", plan, re.IGNORECASE)
    if not match:
        return None
    return match.group(1).strip()


def _list_iteration_tasks_for_context(iteration_id: str) -> List[Dict[str, str]]:
    try:
        return list_workline_tasks(iteration_id=iteration_id, limit=200)
    except APIError:
        return []


def run_specifications(model: ChatOpenAI, discovery: Dict[str, object], plan: str) -> Dict[str, str]:
    system_prompt = (
        "You are a product analyst. Produce a PRD and functional specifications "
        "for ALL features in the plan. Use the discovery context. "
        "Return JSON with keys: prd, gherkin. "
        "PRD should include: overview, goals, non-goals, personas, "
        "user journeys, functional requirements, non-functional requirements, "
        "dependencies, risks, open questions. "
        "Gherkin must include Feature and Scenario blocks for each feature."
    )
    user_input = (
        "Discovery:\n"
        + json.dumps(discovery, indent=2)
        + "\n\nPlan:\n"
        + plan
    )
    output = _run_agent(model, system_prompt, user_input, tools=[])
    parsed = _extract_json_payload(output)
    if not isinstance(parsed, dict):
        return {"prd": output, "gherkin": ""}
    prd = parsed.get("prd", "")
    gherkin = parsed.get("gherkin", "")
    return {"prd": prd, "gherkin": gherkin}


def _slugify(value: str) -> str:
    safe = []
    last_dash = False
    for ch in value.lower():
        if ch.isalnum():
            safe.append(ch)
            last_dash = False
        elif not last_dash:
            safe.append("-")
            last_dash = True
    slug = "".join(safe).strip("-")
    return slug or "feature"


def run_feature_workshops(model: ChatOpenAI, plan: str) -> List[Dict[str, str]]:
    system_prompt = (
        "Extract the feature list from the plan. Return JSON array with "
        "objects: {\"title\": \"...\", \"summary\": \"...\"}. "
        "Only include real product features, not process steps."
    )
    output = _run_agent(model, system_prompt, plan, tools=[])
    parsed = _extract_json_payload(output)
    if not isinstance(parsed, list):
        return []
    items: List[Dict[str, str]] = []
    for entry in parsed:
        if not isinstance(entry, dict):
            continue
        title = str(entry.get("title", "")).strip()
        summary = str(entry.get("summary", "")).strip()
        if title:
            items.append({"title": title, "summary": summary})
    return items


def main() -> None:
    model = ChatOpenAI(model="gpt-4o-mini", temperature=0.2)
    problem_statement = get_problem_statement_from_workline()
    if not problem_statement:
        ensure_problem_refinement_task()
        set_current_workshop("problem-refinement")
        problem_statement = ask_human_question_local(
            "What is the problem statement for this project?",
            options=[
                "Inventory management across plates, regions, and AZs",
                "Incident response control plan and change tracking",
                "Auditability and ownership for server lifecycle",
                "Other (type your own)",
            ],
        )
        set_current_workshop(None)
        set_problem_statement_in_workline(problem_statement)
    discovery = get_problem_refinement_discovery()
    discovery = continue_discovery(model, problem_statement, discovery)

    refinement_task_id = ensure_problem_refinement_task()

    draft_plan = run_planner(model, json.dumps(discovery, indent=2))
    feedback = ask_human_for_review(draft_plan)
    if feedback.lower().startswith("approve"):
        final_plan = draft_plan
    else:
        final_plan = (
            f"{draft_plan}\n\n---\nHuman Review Feedback:\n{feedback}\n"
            "Update the plan to address this feedback."
        )

    feature_workshops = run_feature_workshops(model, final_plan)
    for feature in feature_workshops:
        slug = _slugify(feature["title"])
        task_id = f"workshop-feature-{slug}"
        summary = feature.get("summary") or "Feature workshop for requirements and Gherkin specs."
        ensure_workshop_task(task_id, f"Feature workshop: {feature['title']}", "workshop.clarify", summary)
        set_workshop_output(task_id, summary)
        append_workshop_conversation(task_id, "Auto-generated from plan", summary)
    if feature_workshops:
        client.put_work_outcomes(refinement_task_id, "feature_workshops", feature_workshops)

    specs = run_specifications(model, discovery, final_plan)
    client.put_work_outcomes(refinement_task_id, "prd", specs.get("prd", ""))
    client.put_work_outcomes(refinement_task_id, "gherkin", specs.get("gherkin", ""))

    iteration_id = _extract_iteration_id(final_plan)
    existing_tasks = None
    if iteration_id:
        existing_tasks = _list_iteration_tasks_for_context(iteration_id)
    execution_context = final_plan
    if existing_tasks is not None:
        execution_context = (
            f"{final_plan}\n\nExisting tasks for iteration {iteration_id}:\n"
            + json.dumps(existing_tasks, indent=2)
        )
    result = run_workline(model, execution_context)

    print(
        json.dumps(
            {
                "discovery": discovery,
                "problem_refinement_task_id": refinement_task_id,
                "plan": final_plan,
                "prd": specs.get("prd", ""),
                "gherkin": specs.get("gherkin", ""),
                "workline": result,
            },
            indent=2,
        )
    )


if __name__ == "__main__":
    main()
