package claudeoffline

import (
	"encoding/json"
	"strings"
)

const (
	Port    = 8099
	BaseURL = "http://127.0.0.1:8099"
)

const messagesAPI = `const http = require('http');
const dns = require('dns');
let command = process.env.FAKE_BASH_COMMAND;
const awaitHost = process.env.FAKE_BASH_AWAIT_HOST || '';
let toolCalls = 0;
function awaited(done) {
  if (!awaitHost) { return done(); }
  console.log('==awaiting-host:' + awaitHost);
  const deadline = Date.now() + 60000;
  const attempt = () => dns.lookup(awaitHost, { family: 4 }, (error, address) => {
    if (!error) {
      console.log('==awaited-address:' + address);
      command = command.split('{{address}}').join(address);
      return done();
    }
    if (Date.now() > deadline) { console.log('==awaited-host-unresolved'); return done(); }
    setTimeout(attempt, 200);
  });
  attempt();
}
function offersBash(body) {
  return Array.isArray(body.tools) && body.tools.some((tool) => tool && tool.name === 'Bash');
}
function respond(res, body, content, stopReason) {
  const message = {
    id: 'msg_offline', type: 'message', role: 'assistant', model: body.model || 'offline',
    content: [], stop_reason: null, stop_sequence: null,
    usage: { input_tokens: 1, output_tokens: 1, cache_creation_input_tokens: 0, cache_read_input_tokens: 0 },
  };
  res.writeHead(200, { 'content-type': 'text/event-stream', 'request-id': 'req_offline' });
  const send = (event, data) => res.write('event: ' + event + '\ndata: ' + JSON.stringify(data) + '\n\n');
  send('message_start', { type: 'message_start', message });
  content.forEach((block, index) => {
    if (block.type === 'text') {
      send('content_block_start', { type: 'content_block_start', index, content_block: { type: 'text', text: '' } });
      send('content_block_delta', { type: 'content_block_delta', index, delta: { type: 'text_delta', text: block.text } });
    } else {
      send('content_block_start', { type: 'content_block_start', index, content_block: { type: 'tool_use', id: block.id, name: block.name, input: {} } });
      send('content_block_delta', { type: 'content_block_delta', index, delta: { type: 'input_json_delta', partial_json: JSON.stringify(block.input) } });
    }
    send('content_block_stop', { type: 'content_block_stop', index });
  });
  send('message_delta', { type: 'message_delta', delta: { stop_reason: stopReason, stop_sequence: null }, usage: { output_tokens: 1 } });
  send('message_stop', { type: 'message_stop' });
  res.end();
}
http.createServer((req, res) => {
  let raw = '';
  req.on('data', (chunk) => { raw += chunk; });
  req.on('end', () => {
    let body = {};
    try { body = JSON.parse(raw || '{}'); } catch (error) { body = {}; }
    if (!req.url.startsWith('/v1/messages') || req.url.includes('count_tokens')) {
      res.writeHead(404, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ type: 'error', error: { type: 'not_found_error', message: 'offline' } }));
      return;
    }
    if (offersBash(body) && toolCalls === 0) {
      toolCalls += 1;
      awaited(() => respond(res, body, [{ type: 'tool_use', id: 'toolu_offline_1', name: 'Bash', input: { command, description: 'sandbox probe' } }], 'tool_use'));
      return;
    }
    respond(res, body, [{ type: 'text', text: 'done' }], 'end_turn');
  });
}).listen(8099, '127.0.0.1');
`

func MessagesAPIScript() string {
	return messagesAPI
}

func ToolResult(line string) (string, bool, bool) {
	var event struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
				IsError bool            `json:"is_error"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &event) != nil || event.Type != "user" {
		return "", false, false
	}
	for _, block := range event.Message.Content {
		if block.Type != "tool_result" {
			continue
		}
		var text string
		if json.Unmarshal(block.Content, &text) == nil {
			return text, block.IsError, true
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(block.Content, &parts) == nil {
			var joined strings.Builder
			for _, part := range parts {
				joined.WriteString(part.Text)
			}
			return joined.String(), block.IsError, true
		}
	}
	return "", false, false
}
