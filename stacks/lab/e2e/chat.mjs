// chat.mjs <user> <password> — a person in a real browser: logs in through
// the gate, says something, then asks what they said first. The answer has
// to carry the first message back, which it can only do if the whole
// conversation went page → AG-UI → mind → LiteLLM → model.
//
// On the fake model the question's #first_message tag makes the answer
// exactly the first message; a real model reads the same question in words.
// Then a sum the model has a tool do: on the fake model #tool_sum makes it
// call everything's get-sum with 2 and 3, and the answer carries the tool's
// words back. Then the model thinks before it answers: the page shows its
// reasoning apart from the answer (#think_glm on the fake, in GLM's shape).
// Last, a sum in Gemini's shape (#think_gemini): reasoning, a tool call with
// thought signatures; the fake answers that the signatures came back with
// the call only if they did, and on the next run that both came back again
// from the page, which kept them as the reasoning's encrypted value. A real
// model is only asked to think and add; the signatures are the fake's check.
// Then attachments: a file over the face's limit is refused on the page and
// never sent; a picture and a text file go with a question (#attached), and
// the answer has to carry what only the file holds. Last, the run stopped
// and failed: Stop pressed while the model says nothing yet (#hang) ends
// the run, and its late words never show; a model answering 429 or 500
// (#fail_429, #fail_500, after LiteLLM's retry) ends the run with the mind's
// error, which the page shows. Fake model only: a real one does not fail on
// cue. That the mind saw the stop as a cancel is check.sh's, from its log.
//
// Sound, turn by turn: the mic records what the browser's fake mic plays
// (a tone, or E2E_VOICE_FILE, a WAV) and the transcript lands in the
// composer ("What the fake ear heard." on fake-stt); an answer is read
// aloud: the speech endpoint answers with a sound, the page plays it to
// the end; a speech endpoint that refuses is said on the page. E2E_ONLY=voice
// runs this alone.
//
// A call, sound both ways (when LAB_REALTIME_MODEL is set): the page calls
// the mind's /api/v1/realtime; the fake mic's sound goes out as PCM16, the
// fake model (fake-realtime) hears it as an utterance and answers from its
// script; both transcripts land in the thread as a voice conversation, the
// answer's sound comes and plays; the next tone while a longer answer plays
// is an interruption, and the page tells the model how much was heard
// (truncate); muted, nothing goes out; hung up, the call closes. On
// fake-realtime the interruption is seen as the answer done before its
// sound (what Gemini sent), on fake-realtime-full as speech_started.
// E2E_ONLY=call runs this alone.
//
// E2E_ONLY=refused is another round, on a mind asking a model that takes no
// pictures (fake-text): a picture sent ends the run with the mind's error,
// and the page shows it.
import { chromium } from 'playwright'
import { writeFileSync } from 'node:fs'

const [user, password] = process.argv.slice(2)
const url = process.env.LAB_URL
const first = process.env.E2E_FIRST ?? 'даром за амбаром'
const question = 'What was my very first message in this chat? Repeat it word for word. #first_message'
const sum = 'Use the get-sum tool to add 2 and 3, and tell me what it said. #tool_sum'
const think = 'Think it over first: is 1001 a prime number? #think_glm'
const signed = 'Think, then use the get-sum tool to add 2 and 3. #think_gemini'
const back = 'Did my signatures come back? #signatures_back'
const attached = 'What does the attached note say? Quote its code. #attached'
const secret = 'ZEBRA-7'
const note = { name: 'note.txt', mimeType: 'text/plain', buffer: Buffer.from(`The code is ${secret}.\n`) }
const dot = {
  name: 'dot.png',
  mimeType: 'image/png',
  buffer: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg==', 'base64'),
}
const huge = { name: 'huge.txt', mimeType: 'text/plain', buffer: Buffer.alloc(11 * 1024 * 1024, 'a') }
const fake = (process.env.LAB_LLM_MODEL ?? 'fake') === 'fake'
const fakeEar = (process.env.LAB_STT_MODEL || 'fake-stt') === 'fake-stt'
const timeout = Number(process.env.E2E_TIMEOUT_MS ?? 60000)
const only = process.env.E2E_ONLY ?? ''
const realtime = process.env.LAB_REALTIME_MODEL ?? ''

// tone is a WAV of a second of 440 Hz and two of quiet, for the fake mic
// when no file is given: played over and over, an utterance each time.
function tone(path) {
  const rate = 16000, n = rate * 3, data = Buffer.alloc(44 + n * 2)
  data.write('RIFF', 0); data.writeUInt32LE(36 + n * 2, 4); data.write('WAVEfmt ', 8)
  data.writeUInt32LE(16, 16); data.writeUInt16LE(1, 20); data.writeUInt16LE(1, 22)
  data.writeUInt32LE(rate, 24); data.writeUInt32LE(rate * 2, 28); data.writeUInt16LE(2, 32)
  data.writeUInt16LE(16, 34); data.write('data', 36); data.writeUInt32LE(n * 2, 40)
  for (let i = 0; i < rate; i++) data.writeInt16LE(Math.round(8000 * Math.sin(2 * Math.PI * 440 * i / rate)), 44 + i * 2)
  writeFileSync(path, data)
  return path
}
const voiceFile = process.env.E2E_VOICE_FILE || tone('/tmp/tone.wav')

const browser = await chromium.launch({
  args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', `--use-file-for-fake-audio-capture=${voiceFile}`, '--autoplay-policy=no-user-gesture-required'],
})
const context = await browser.newContext({ ignoreHTTPSErrors: true, permissions: ['microphone'] })
const page = await context.newPage()
page.setDefaultTimeout(timeout)
let failed = false
const step = (label, value) => console.log(`  ${label.padEnd(36)} ${value}`)

// say writes text and waits for the run to end; the answer is what the
// assistant said since, which may be more than one message: a turn with a
// tool call is shown apart from the words that follow its result.
async function say(text) {
  const before = await page.locator('[data-role="assistant"]').count()
  await page.getByRole('textbox', { name: 'Message input' }).fill(text)
  await page.keyboard.press('Enter')
  await page.locator('[data-role="assistant"]').nth(before).waitFor()
  await page.getByRole('button', { name: 'Send message' }).waitFor()
  // The page types an answer out smoothly, behind the stream: the run is
  // over before the words are all on screen. Read once they stand still.
  let last = null
  for (;;) {
    const all = await page.locator('[data-role="assistant"]').allInnerTexts()
    const now = all.slice(before).join('\n').trim()
    if (now === last) return now
    last = now
    await page.waitForTimeout(500)
  }
}

// attach puts files on the message being written, through the page's own
// attach button.
async function attach(...files) {
  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: 'Add Attachment' }).click()
  await (await chooser).setFiles(files)
}

// reasoned is how many reasoning blocks the page shows, and the words of the
// latest when opened.
async function reasoned() {
  const triggers = page.locator('[data-slot="reasoning-trigger"]')
  const n = await triggers.count()
  if (n === 0) return { n, text: '' }
  await triggers.nth(n - 1).click()
  const content = page.locator('[data-slot="reasoning-content"]').last()
  await content.waitFor()
  return { n, text: (await content.innerText()).trim() }
}

// stoppedAndFailed: Stop mid-run, then a model that fails, twice.
async function stoppedAndFailed() {
  const before = await page.locator('[data-role="assistant"]').count()
  await page.getByRole('textbox', { name: 'Message input' }).fill('Take your time. #hang')
  await page.keyboard.press('Enter')
  await page.getByRole('button', { name: 'Stop generating' }).click()
  await page.getByRole('button', { name: 'Send message' }).waitFor()
  await page.waitForTimeout(5000) // past when the late answer would have come
  const late = (await page.locator('[data-role="assistant"]').allInnerTexts()).slice(before).join('\n')
  step('stopped while the model was silent', JSON.stringify(late.slice(0, 60)))
  if (late.includes('arrived late')) {
    step('FAIL', 'the answer came after Stop')
    failed = true
  }
  for (const [tag, status] of [['#fail_429', '429'], ['#fail_500', '500']]) {
    const said = await say(`Say anything. ${tag}`)
    step(`a model answering ${status}`, JSON.stringify(said.slice(0, 80)))
    if (!said.includes(`answered ${status}`)) {
      step('FAIL', `the page does not say the model answered ${status}`)
      failed = true
    }
  }
}

// voiceRound: the mic into the composer, then an answer read aloud.
async function voiceRound() {
  const box = page.getByRole('textbox', { name: 'Message input' })
  await box.fill('')
  await page.getByRole('button', { name: 'Start voice input' }).click()
  await page.getByRole('button', { name: 'Stop voice input' }).waitFor()
  await page.waitForTimeout(2000)
  const heard = page.waitForResponse((r) => r.url().endsWith('/api/v1/audio/transcriptions'))
  heard.catch(() => {}) // awaited below; a click that fails first is the error said
  await page.getByRole('button', { name: 'Stop voice input' }).click()
  const resp = await heard
  await page.getByRole('button', { name: 'Start voice input' }).waitFor()
  const text = await box.inputValue()
  step('dictated, in the composer', `${resp.status()} ${JSON.stringify(text.slice(0, 60))}`)
  if (resp.status() !== 200 || !text || (fakeEar && text !== 'What the fake ear heard.')) {
    step('FAIL', 'the transcript did not land in the composer')
    failed = true
  }

  const said = await say(text || 'Say something.')
  step('its answer', JSON.stringify(said.slice(0, 40)))
  const answer = page.locator('[data-role="assistant"]').last()
  await answer.hover()
  const spoken = page.waitForResponse((r) => r.url().endsWith('/api/v1/audio/speech'))
  spoken.catch(() => {})
  await answer.getByRole('button', { name: 'Read aloud' }).click()
  const sound = await spoken
  const bytes = (await sound.body()).length
  step('read aloud', `${sound.status()} ${sound.headers()['content-type']}, ${bytes} bytes`)
  if (sound.status() !== 200 || !sound.headers()['content-type']?.startsWith('audio/') || bytes < 100) {
    step('FAIL', 'no sound came back')
    failed = true
    return
  }
  // Playing, then played to the end: the stop button comes and goes.
  await answer.getByRole('button', { name: 'Stop reading' }).waitFor()
  await answer.getByRole('button', { name: 'Read aloud' }).waitFor()
  step('played to the end', 'yes')

  // A speech endpoint that refuses: the page says so, in its words.
  await page.route('**/api/v1/audio/speech', (route) =>
    route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: { message: 'no such voice', code: '400' } }) }))
  await answer.getByRole('button', { name: 'Read aloud' }).click()
  const notice = page.getByRole('alert').filter({ hasText: 'no such voice' })
  await notice.waitFor()
  step('a refusal, said', JSON.stringify(await notice.innerText()))
  await page.unroute('**/api/v1/audio/speech')
}

// callRound: a call through the mind, on the fake realtime model.
async function callRound() {
  const sent = [], got = []
  page.on('websocket', (ws) => {
    if (!ws.url().endsWith('/api/v1/realtime')) return
    ws.on('framesent', (f) => { try { sent.push({ t: Date.now(), ...JSON.parse(f.payload) }) } catch {} })
    ws.on('framereceived', (f) => { try { got.push(JSON.parse(f.payload)) } catch {} })
  })
  const until = async (what, test, ms = timeout) => {
    const end = Date.now() + ms
    while (Date.now() < end) {
      if (test()) return
      await page.waitForTimeout(100)
    }
    throw new Error(`no ${what} within ${ms} ms`)
  }
  const count = (list, type) => list.filter((e) => e.type === type).length

  await page.getByRole('button', { name: 'Start a call' }).click()
  await page.getByRole('button', { name: 'Hang up' }).waitFor()
  await until('session', () => count(got, 'session.created') > 0)
  step('a call, opened', `${got.find((e) => e.type === 'session.created').session?.model ?? '?'}`)

  const user = page.locator('[data-slot="aui_spoken-message-root"][data-role="user"]')
  const assistant = page.locator('[data-slot="aui_spoken-message-root"][data-role="assistant"]')
  await user.first().waitFor()
  await assistant.first().waitFor()
  await until('a whole answer', () => count(got, 'response.done') > 0)
  const heard = (await user.first().innerText()).trim(), answered = (await assistant.first().innerText()).trim()
  step('what was heard, in the thread', JSON.stringify(heard.slice(-60)))
  step('what was answered, in the thread', JSON.stringify(answered.slice(-60)))
  const sound = count(got, 'response.output_audio.delta')
  step('sound out, sound back', `${count(sent, 'input_audio_buffer.append')} chunks out, ${sound} back`)
  if (!heard.includes('capital of France') || !answered.includes('Paris') || !sound) {
    step('FAIL', 'the call did not carry both sides')
    failed = true
  }

  await until('an interruption told to the model', () => count(sent, 'conversation.item.truncate') > 0)
  const cut = sent.find((e) => e.type === 'conversation.item.truncate')
  step('interrupted, heard of the answer', `${cut.audio_end_ms} ms`)
  if (!(cut.audio_end_ms > 0)) {
    step('FAIL', 'the truncate says nothing was heard')
    failed = true
  }

  await page.getByRole('button', { name: 'Mute' }).click()
  await page.getByRole('button', { name: 'Unmute' }).waitFor()
  await page.waitForTimeout(300)
  const muted = count(sent, 'input_audio_buffer.append')
  await page.waitForTimeout(1500)
  step('muted: sound out in 1.5 s', `${count(sent, 'input_audio_buffer.append') - muted} chunks`)
  if (count(sent, 'input_audio_buffer.append') !== muted) {
    step('FAIL', 'sound went out while muted')
    failed = true
  }
  await page.getByRole('button', { name: 'Unmute' }).click()

  await page.getByRole('button', { name: 'Hang up' }).click()
  await page.getByRole('button', { name: 'Start a call' }).waitFor()
  step('hung up', `${await user.count()} heard, ${await assistant.count()} answered in the thread`)
}

// modelRound: the picker offers the lab's chat groups, the mind's default
// chosen; a pick is the group that answers, and picking back the default
// goes back to it. In a conversation of its own (a reload: the page keeps
// it in memory), since a picture said before would be refused by fake-text.
async function modelRound() {
  await page.reload()
  await page.getByRole('textbox', { name: 'Message input' }).waitFor()
  const picker = page.getByRole('combobox', { name: /^Model:/ })
  const chosen = await picker.getAttribute('aria-label')
  await picker.click()
  const offered = await page.getByRole('option').allInnerTexts()
  step('offered, chosen', `${offered.join(' ')}; ${chosen}`)
  if (chosen !== 'Model: fake' || !offered.includes('fake') || !offered.includes('fake-text') || offered.some((m) => m.includes('stt') || m.includes('realtime'))) {
    step('FAIL', 'not the chat groups, or not fake chosen')
    failed = true
  }
  await page.getByRole('option', { name: 'fake-text', exact: true }).click()
  const picked = await say('Which model are you? #which_model')
  step('picked fake-text, answered', JSON.stringify(picked))
  await picker.click()
  await page.getByRole('option', { name: 'fake', exact: true }).click()
  const back = await say('And now? #which_model')
  step('picked fake again, answered', JSON.stringify(back))
  if (!picked.includes('fake-text-1') || !back.includes('fake-1') || back.includes('fake-text')) {
    step('FAIL', 'the pick did not decide which group answered')
    failed = true
  }
}

// threadsRound: the sidebar's chats. A new one starts empty and runs as a
// thread of its own; the first is all there when switched back to.
async function threadsRound() {
  await page.reload()
  await page.getByRole('textbox', { name: 'Message input' }).waitFor()
  const user = page.locator('[data-role="user"]')
  await say('the first chat')
  const ids = []
  page.on('request', (r) => { if (r.url().endsWith('/api/run')) ids.push(JSON.parse(r.postData() ?? '{}').threadId) })
  await page.getByRole('button', { name: 'New Thread' }).click()
  const empty = await user.count()
  await say('the second chat')
  const second = await user.allInnerTexts()
  await page.getByText('the first chat', { exact: true }).first().click()
  await page.getByText('the second chat').first().waitFor({ state: 'detached' }).catch(() => {})
  const first = await user.allInnerTexts()
  await say('back in the first')
  step('a new chat, then back', `${empty} messages in the new one; then ${JSON.stringify(first)}`)
  if (empty !== 0 || second.join() !== 'the second chat' || first.join() !== 'the first chat' || ids.length !== 2 || ids[0] === ids[1]) {
    step('FAIL', `chats not apart: ${JSON.stringify({ empty, second, first, ids })}`)
    failed = true
  }
}

// refusedRound: a picture to a model that takes none; the page says so.
async function refusedRound() {
  await attach(dot)
  const said = await say('What is in the picture?')
  step('a picture to a text-only model', JSON.stringify(said.slice(0, 80)))
  if (!said.includes('does not take pictures (dot.png)')) {
    step('FAIL', 'the page does not say the model takes no pictures')
    failed = true
  }
}

// fullRound: the conversation, tools, reasoning, attachments.
async function fullRound() {
  const a1 = await say(first)
  step('an answer', JSON.stringify(a1.slice(0, 40)))
  const a2 = await say(question)
  step('the first message, asked back', JSON.stringify(a2.slice(0, 80)))
  if (!a2.toLowerCase().includes(first.toLowerCase())) {
    step('FAIL', `the answer does not carry ${JSON.stringify(first)}`)
    failed = true
  }
  const a3 = await say(sum)
  step('a sum, done by a tool', JSON.stringify(a3.slice(-80)))
  if (!a3.includes('5')) {
    step('FAIL', 'the answer does not carry the sum, 5')
    failed = true
  }

  const before = (await reasoned()).n
  const a4 = await say(think)
  const r = await reasoned()
  step('thinks, then answers', `${r.n - before} reasoning, ${JSON.stringify(r.text.slice(0, 40))}`)
  if (r.n <= before || !r.text) {
    step('FAIL', 'no reasoning shown before the answer')
    failed = true
  }
  if (fake && (!a4.includes('Thought it over.') || r.text.includes('Thought it over.'))) {
    step('FAIL', `reasoning and answer not apart: ${JSON.stringify(a4.slice(-80))}`)
    failed = true
  }

  const a5 = await say(signed)
  step('a sum, thought out, by a tool', JSON.stringify(a5.slice(-60)))
  const a6 = await say(back)
  step('the signatures, a run later', JSON.stringify(a6.slice(-60)))
  if (fake && !a5.includes('its signature came back')) {
    step('FAIL', 'the thought signatures did not go back with the tool call')
    failed = true
  }
  if (fake && !a6.includes('Both signatures came back')) {
    step('FAIL', 'the thought signatures did not come back from the page')
    failed = true
  }

  // Over the limit: the page says why when the person points at the file,
  // and pressing send sends nothing while it is there.
  let runs = 0
  page.on('request', (r) => { if (r.url().endsWith('/api/run')) runs++ })
  await attach(huge)
  await page.getByLabel('Document attachment huge.txt, upload failed').hover({ position: { x: 12, y: 44 } })
  const refused = page.getByText(/Too large: 11\.0 MB/).first()
  await refused.waitFor()
  step('a file over the limit', await refused.innerText())
  const sent = runs
  await page.getByRole('textbox', { name: 'Message input' }).fill('this goes nowhere')
  await page.keyboard.press('Enter')
  await page.waitForTimeout(2000)
  step('sent with it', `${runs - sent} runs`)
  if (runs !== sent) {
    step('FAIL', 'a message went out with a file over the limit')
    failed = true
  }
  await page.getByRole('button', { name: 'Remove huge.txt' }).click()
  await page.getByLabel(/attachment huge\.txt/).waitFor({ state: 'detached' })

  await attach(dot, note)
  const a7 = await say(attached)
  step('a picture and a file, attached', JSON.stringify(a7.slice(0, 80)))
  if (!a7.includes(secret)) {
    step('FAIL', `the answer does not carry what the file holds, ${secret}`)
    failed = true
  }

  if (fake) await stoppedAndFailed()
}

try {
  await page.goto(url + '/')
  step('no session: the login page', new URL(page.url()).pathname)
  await page.locator('#username-textfield').fill(user)
  await page.locator('#password-textfield').fill(password)
  await page.locator('#sign-in-button').click()
  await page.getByRole('textbox', { name: 'Message input' }).waitFor()
  step(`${user} logs in, the chat`, await page.title())

  if (only === 'refused') await refusedRound()
  else if (only === 'voice') await voiceRound()
  else if (only === 'call') await callRound()
  else if (only === 'model') await modelRound()
  else if (only === 'threads') await threadsRound()
  else {
    await fullRound()
    if (fake) await modelRound()
    await threadsRound()
    await voiceRound()
    if (realtime) await callRound()
  }
} catch (e) {
  console.log(`  FAIL ${e.message.split('\n')[0]}`)
  await page.screenshot({ path: '/tmp/e2e-fail.png' }).catch(() => {})
  failed = true
} finally {
  await browser.close()
}
process.exit(failed ? 1 : 0)
