"""Run the actual Hermes core in a fresh process; credentials stay in the relay."""
import json, os, sys
os.environ["HERMES_HOME"]=os.environ["BENCH_HOME"]
from run_agent import AIAgent
agent = AIAgent(base_url=os.environ['BENCH_BASE_URL'], api_key='benchmark-relay',
 provider='custom', api_mode='chat_completions', model='azm', max_iterations=20,
 enabled_toolsets=['terminal','file'], quiet_mode=True, skip_memory=True,
 skip_background_review=True, skip_context_files=True, cwd=os.environ['BENCH_TASK_DIR'],
 run_budget_seconds=180)
agent._disable_streaming = True
result = agent.run_conversation(sys.stdin.read())
print(json.dumps({'final_response':result.get('final_response'),'tokens':agent.session_total_tokens},default=str))
