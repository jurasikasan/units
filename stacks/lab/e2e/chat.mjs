// chat.mjs <user> <password> — a person in a real browser: logs in through
// the gate, says something, then asks what they said first. The page writes
// into threads through reins as the person's avatar; the driver holding
// <user>-assistant's reins is woken, reads the thread back, has the mind
// answer (through LiteLLM, the model) and says the answer in the thread;
// its pieces come to the page on reins' stream as they are written. The
// answer has to carry the first message back, which it can only do if the
// conversation went page → reins → threads → driver → mind → model.
//
// On the fake model the question's #first_message tag makes the answer
// the first user message ("<user>: …", who said it in the words); a real
// model reads the same question in words. Then a sum the model has a tool
// do (#tool_sum: everything's get-sum with 2 and 3). Then the model thinks
// before it answers (#think_glm): the reasoning is shown while written, the
// reply said is the answer alone. A sum in Gemini's shape (#think_gemini):
// the signatures go back with the tool call within the run; a run later
// they are gone, not being what was said. Then attachments: a file over
// the face's limit is refused on the page and nothing is written; a picture
// and a text file go with a question (#attached), as threads' blocks, and
// the answer has to carry what only the file holds. Last, a model answering
// 429 or 500 (#fail_429, #fail_500): the driver says so while writing, the
// page shows it, and a reload shows it gone: never journaled. Fake model
// only: a real one does not fail on cue.
//
// The chats are threads: a new one is opened on its first words, the first
// is all there when switched back to, and a reload keeps the conversation
// and the thread in the address (/t/<thread>) (E2E_ONLY=threads).
//
// Who the page is (UNIT-73): every login lands as the ghost, the threads
// listed, every message on the left with its sender's name and time, no
// composer, no New thread; then it picks <user> in the sidebar's picker
// and "this device" in its radio, and the composer comes. On the first
// login the assistant, held by the fake profile, greets first in the
// thread it opened. Two browsers of the same person (E2E_ONLY=two): the
// second picks <user>-assistant and takes it, and the two talk, each
// seeing the other's words without a reload, no brain answering; the
// second may not write as <user> (409, held on another device); it hands
// the assistant to the fake profile, which answers; it takes it back, and
// the brain says nothing more. Last, the first picks the ghost again: the
// avatar it held is given back to nobody, and the assistant, stepped into
// and left while the fake profile holds it, stays held by it. A reply
// being written is stopped from the
// composer's row: what was written is kept, and nothing when nothing was
// (E2E_ONLY=stop).
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
// script; both transcripts show in the chat as a voice conversation (for
// as long as the page is open: a call is not journaled yet, UNIT-52), the
// answer's sound comes and plays; the next tone while a longer answer plays
// is an interruption, and the page tells the model how much was heard
// (truncate); muted, nothing goes out; hung up, the call closes. On
// fake-realtime the interruption is seen as the answer done before its
// sound (what Gemini sent), on fake-realtime-full as speech_started.
// E2E_ONLY=call runs this alone. Last, the sidebar's foot shows who is
// logged in, and its log-out ends the session (E2E_ONLY=me).
//
// The model that thinks is the driver's, not the page's (E2E_ONLY=model). An ear, a mouth and a
// voice are an avatar's body, the same fields on every avatar, set in the
// settings opened from the sidebar's foot (kept in subjects, through
// reins): the page wires the classic chat for now — alice's ear hears,
// the other avatar's mouth and voice speak (E2E_ONLY=sound), and on
// cascade they are the parts of a call.
//
// E2E_ONLY=refused is another round: the assistant handed to a profile whose
// model takes no pictures (fake-text), a picture sent ends the reply with
// the mind's error, said while writing, and the page shows it; then back
// to fake.
//
// Each round is a scenario, run alone by name with E2E_ONLY=<name> (the
// list is at the foot, `scenarios`): greet, conversation (the conversation,
// tools, reasoning, attachments, failures), stop, model, threads, voice,
// sound, call, me, two, refused, failed. check.sh runs them one by one,
// each its own run and its own line; e2e/repeat.sh runs one many times.
// On a failure, what reins holds now is printed beside what the page shows.
import { chromium } from 'playwright'
import { writeFileSync } from 'node:fs'

const [user, password] = process.argv.slice(2)
const assistant = `${user}-assistant`
const url = process.env.LAB_URL
const first = process.env.E2E_FIRST ?? 'даром за амбаром'
const question = 'What was my very first message in this chat? Repeat it word for word. #first_message'
const sum = 'Use the get-sum tool to add 2 and 3, and tell me what it said. #tool_sum'
const think = 'Think it over first: is 1001 a prime number? #think_glm'
const signed = 'Think, then use the get-sum tool to add 2 and 3. #think_gemini'
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

// say writes text and waits for the other side's answer: the driver holding
// the assistant's reins writes it (its pieces come as they are written,
// "… is writing" beside the composer) and says it in the thread. The answer
// is what the other side showed since: the said entry, or a failure said
// while writing.
async function say(text) {
  const before = await page.locator('[data-role="assistant"]').count()
  await page.getByRole('textbox', { name: 'Message input' }).fill(text)
  await page.keyboard.press('Enter')
  await page.locator('[data-role="assistant"]').nth(before).waitFor()
  // The page types words out smoothly, behind the stream. Read once nobody
  // writes and the words stand still.
  let last = null
  for (;;) {
    const writing = await page.getByRole('status', { name: 'Writing' }).count()
    const all = await page.locator('[data-role="assistant"]').allInnerTexts()
    const now = all.slice(before).join('\n').trim()
    if (!writing && now === last) return now
    last = now
    await page.waitForTimeout(700)
  }
}

// fresh opens a new chat: nothing said there yet.
async function fresh() {
  await page.getByRole('button', { name: 'New Thread' }).click()
  // Nothing from either side: a thread of the assistant's words alone (its
  // greeting) is not mistaken for the new one.
  await page.locator('[data-role="user"]').first().waitFor({ state: 'detached' })
  await page.locator('[data-role="assistant"]').first().waitFor({ state: 'detached' })
}

// attach puts files on the message being written, through the page's own
// attach button.
async function attach(...files) {
  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: 'Add Attachment' }).click()
  await (await chooser).setFiles(files)
}

// failedRound: a model that fails, twice. The driver says so while writing,
// never in the journal: after a reload it is gone, and the person's words
// are still there. Nothing stops a reply being written yet. A brain that
// failed lets go of the avatar (UNIT-94, fail fast): the page shows it held
// by nobody. A brain handed it again answers what was said last and left
// unanswered, which would fail again: the person says something else first,
// then hands it to the fake profile, which answers that.
async function failedRound() {
  for (const [tag, status] of [['#fail_429', '429'], ['#fail_500', '500']]) {
    const said = await say(`Say anything. ${tag}`)
    step(`a model answering ${status}`, JSON.stringify(said.slice(0, 80)))
    if (!said.includes(`answered ${status}`)) {
      step('FAIL', `the page does not say the model answered ${status}`)
      failed = true
    }
    await heldBy(page, assistant, 'nobody')
    await page.getByRole('textbox', { name: 'Message input' }).fill(`Never mind ${status}.`)
    await page.keyboard.press('Enter')
    await page.getByText(`Never mind ${status}.`).first().waitFor()
    await handOther('fake')
    await page.waitForFunction(() => {
      const said = [...document.querySelectorAll('[data-role="user"], [data-role="assistant"]')]
      const last = said.at(-1)
      return last?.dataset.role === 'assistant' && !last.innerText.includes('answered')
    })
    await page.getByRole('status', { name: 'Writing' }).waitFor({ state: 'detached' })
    step('the brain failed, the assistant', 'held by nobody; handed to fake again, it answers')
  }
  await page.reload()
  await page.getByText('Say anything. #fail_500').first().waitFor()
  const kept = (await page.locator('[data-role="assistant"]').allInnerTexts()).join('\n')
  step('after a reload, the failure', kept.includes('answered 500') ? 'still there' : 'gone, never journaled')
  if (kept.includes('answered 500')) {
    step('FAIL', 'a failure was journaled')
    failed = true
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

  // Ours is made of parts, and they are the ones the avatars prefer: the
  // person's ear the second fake one, the assistant's mouth the second
  // fake one in its voice irina.
  if (realtime === 'cascade') {
    await settings(user, { 'Speech to text': 'fake-stt-b' })
    await settings(assistant, { 'Text to speech': 'fake-tts-b', Voice: 'irina' })
    step('preferred for the call', 'fake-stt-b, fake-tts-b irina')
  }
  await page.getByRole('button', { name: 'Start a call' }).click()
  await page.getByRole('button', { name: 'Hang up' }).waitFor()
  await until('session', () => count(got, 'session.created') > 0)
  step('a call, opened', `${got.find((e) => e.type === 'session.created').session?.model ?? '?'}`)

  const saidBy = page.locator('[data-slot="aui_spoken-message-root"][data-role="user"]')
  const answeredBy = page.locator('[data-slot="aui_spoken-message-root"][data-role="assistant"]')
  await saidBy.first().waitFor()
  await answeredBy.first().waitFor()
  await until('a whole answer', () => count(got, 'response.done') > 0)
  const heard = (await saidBy.first().innerText()).trim(), answered = (await answeredBy.first().innerText()).trim()
  step('what was heard, in the thread', JSON.stringify(heard.slice(-60)))
  step('what was answered, in the thread', JSON.stringify(answered.slice(-60)))
  const sound = count(got, 'response.output_audio.delta')
  step('sound out, sound back', `${count(sent, 'input_audio_buffer.append')} chunks out, ${sound} back`)
  // The scripted fakes answer from their script; ours (cascade) is the
  // preferred ear's words and the fake model's answer, through hearing and speaking.
  const [ear, mouth] = realtime === 'cascade' ? ['second fake ear heard', 'fake LLM'] : ['capital of France', 'Paris']
  if (!heard.includes(ear) || !answered.includes(mouth) || !sound) {
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
  const kept = [await saidBy.count(), await answeredBy.count()]
  step('hung up', `${kept[0]} heard, ${kept[1]} answered in the chat`)
  if (!kept[0] || !kept[1]) {
    step('FAIL', 'what was said in the call left the chat at hang-up')
    failed = true
  }
  // A call's transcripts are not journaled yet (UNIT-52): a reload loses them.
  await page.reload()
  await page.getByRole('textbox', { name: 'Message input' }).waitFor()
  await page.waitForTimeout(1500)
  step('after a reload, the call', `${await saidBy.count()} heard: not journaled yet (UNIT-52)`)
}

// modelRound: which model thinks is the brain holding the reins, the
// driver's (LAB_LLM_MODEL), not the page's: the composer offers no pick,
// and the answer says which group answered. An avatar's profile is the
// settings': a name set there is still there after a reload, and every
// avatar offers the same, none a chat model.
async function modelRound() {
  await fresh()
  const pickers = await page.getByRole('combobox', { name: /^Chat model:/ }).count()
  const which = await say('Which model are you? #which_model')
  step('no pick in the composer; answered', `${pickers} pickers; ${JSON.stringify(which)}`)
  if (pickers !== 0 || !which.includes('fake-1') || which.includes('fake-text')) {
    step('FAIL', 'a model picked in the page, or not the driver\'s answered')
    failed = true
  }
  const bodies = await settings(assistant, {}, { Name: 'Lab helper' })
  await page.reload()
  await page.getByRole('textbox', { name: 'Message input' }).waitFor()
  const kept = await settings(assistant, {}, {}, ['Name'])
  step('a name set, after the reload', JSON.stringify(kept.read.Name))
  if (kept.read.Name !== 'Lab helper' || bodies === undefined) {
    step('FAIL', 'what was saved did not survive the reload')
    failed = true
  }
  const alike = await settingsLabels([user, assistant])
  step('every avatar offers the same', JSON.stringify(alike))
  if (alike[user] !== alike[assistant] || alike[user].includes('Chat model')) {
    step('FAIL', 'avatars are not alike in the settings, or a body has a chat model')
    failed = true
  }
}

// settingsLabels opens each nick's avatar in the settings and returns the
// labels of its pickers, joined.
async function settingsLabels(nicks) {
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Settings' })
  await dialog.waitFor()
  const out = {}
  for (const nick of nicks) {
    await dialog.getByRole('button', { name: nick, exact: true }).click()
    const form = dialog.getByRole('form', { name: `Avatar ${nick}` })
    await form.waitFor()
    out[nick] = (await form.getByRole('combobox').evaluateAll((els) => els.map((e) => e.getAttribute('aria-label').split(':')[0]))).join(',')
  }
  await page.keyboard.press('Escape')
  await dialog.waitFor({ state: 'detached' })
  return out
}

// threadsRound: the sidebar's chats are threads. A new one starts empty
// and is opened as a thread of its own on its first words; the first is
// all there when switched back to; a reload keeps the conversation, answer
// and all, and shows the newest chat.
async function threadsRound() {
  const userSaid = page.locator('[data-role="user"]')
  const opened = []
  page.on('request', (r) => { if (r.method() === 'POST' && r.url().endsWith('/api/reins/me/threads')) opened.push(r.url()) })
  await fresh()
  await say('the first chat')
  await fresh()
  const empty = await userSaid.count()
  const answer = await say('the second chat')
  const second = await userSaid.allInnerTexts()
  await page.getByText('the first chat', { exact: true }).first().click()
  await page.getByText('the second chat').first().waitFor({ state: 'detached' }).catch(() => {})
  const first = await userSaid.allInnerTexts()
  step('a new chat, then back', `${empty} messages in the new one; then ${JSON.stringify(first)}`)
  if (empty !== 0 || second.join().trim() !== 'the second chat' || first.join().trim() !== 'the first chat' || opened.length !== 2) {
    step('FAIL', `chats not apart: ${JSON.stringify({ empty, second, first, opened: opened.length })}`)
    failed = true
  }
  await say('back in the first')
  const before = new URL(page.url()).pathname
  await page.reload()
  await page.getByText('back in the first').first().waitFor()
  const after = new URL(page.url()).pathname
  step('the address, reloaded', `${before} -> ${after}`)
  if (!/^\/t\/[^/]+$/.test(before) || after !== before) {
    step('FAIL', 'the thread is not in the address, or a reload left it')
    failed = true
  }
  const kept = await userSaid.allInnerTexts()
  const answered = await page.locator('[data-role="assistant"]').count()
  step('after a reload', `${JSON.stringify(kept.map((t) => t.trim()))}, ${answered} answers`)
  if (kept.map((t) => t.trim()).join() !== 'the first chat,back in the first' || answered !== 2 || !answer) {
    step('FAIL', 'the reload did not keep the conversation')
    failed = true
  }
}

// picker is the top of the sidebar: who the page is on this device, the
// ghost or one of the person's avatars, each with who holds its reins.
const picker = (p) => p.getByRole('radiogroup', { name: 'Who I am here' })
const pickerItem = (p, nick) =>
  nick === 'Ghost' ? picker(p).getByRole('radio', { name: 'Ghost', exact: true }) : picker(p).getByRole('radio', { name: new RegExp(`^${nick}: `) })

// heldBy waits until the page shows the avatar held by `label` ("you,
// here", "you, on another device", "brain <profile>", "nobody").
async function heldBy(p, nick, label) {
  await picker(p).getByRole('radio', { name: `${nick}: ${label}`, exact: true }).waitFor()
}

// be picks who the page is on this device: "Ghost", or an avatar's nick.
// Leaving an avatar held here gives it back.
async function be(p, nick) {
  await pickerItem(p, nick).click()
  await pickerItem(p, nick).and(p.locator('[aria-checked="true"]')).waitFor()
}

// brain picks, in the radio under the avatar picked, who is in it: "none"
// (give back), "this device" (take), or a profile (hand).
async function brain(p, nick, option) {
  const radio = p.getByRole('group', { name: `${nick}'s brain` }).getByRole('radio', { name: option, exact: true })
  await radio.click()
  await radio.and(p.locator(':checked')).waitFor()
}

// ghostSees: a fresh login lands as the ghost: the threads listed, every
// message on the left with its sender's name and time, no composer, no New
// thread.
async function ghostSees(p) {
  await p.locator('[data-slot="aui_thread-list-item"]').first().waitFor()
  await p.locator('[data-role="assistant"]').first().waitFor()
  await p.waitForTimeout(1000)
  const threads = await p.locator('[data-slot="aui_thread-list-item"]').count()
  const left = await p.locator('[data-role="assistant"]').count()
  const right = await p.locator('[data-role="user"]').count()
  const named = await p.locator('[data-role="assistant"]:has([data-slot="said-by"] time)').count()
  const composer = await p.getByRole('textbox', { name: 'Message input' }).count()
  const fresh = await p.getByRole('button', { name: 'New Thread' }).count()
  step('lands as the ghost', `${threads} threads; ${left} on the left, ${named} with name and time, ${right} on the right; composer ${composer}, New thread ${fresh}`)
  if (!threads || !left || right || named !== left || composer || fresh) {
    step('FAIL', 'the ghost does not see as it should')
    failed = true
  }
}

// logIn logs a page in through the gate; it lands as the ghost. Then,
// unless `watch`, it steps into the person's own avatar and takes it on
// this device: the composer comes.
async function logIn(p, watch = false) {
  await p.goto(url + '/')
  await p.locator('#username-textfield').fill(user)
  await p.locator('#password-textfield').fill(password)
  await p.locator('#sign-in-button').click()
  await picker(p).getByRole('radio', { name: 'Ghost', checked: true }).waitFor()
  await ghostSees(p)
  if (watch) return
  await be(p, user)
  await brain(p, user, 'this device')
  await heldBy(p, user, 'you, here')
  await p.getByRole('textbox', { name: 'Message input' }).waitFor()
}

// greetRound: on the first login the newest chat is the first thread,
// opened by the assistant, which speaks first: nothing said by the person.
async function greetRound() {
  const first = page.locator('[data-role="assistant"]').first()
  await first.waitFor()
  const mine = await page.locator('[data-role="user"]').count()
  step('the assistant greets first', JSON.stringify((await first.innerText()).slice(0, 40)))
  if (mine !== 0) {
    step('FAIL', 'the person said something before the greeting')
    failed = true
  }
}

// twoRound: two browsers of the same person, the AC of UNIT-67. The
// second watches <user> (held by the first) and takes <user>-assistant's
// reins; the two talk, each seeing the other's words without a reload, on
// the other side, and no brain answers. Writing as <user> from the second
// is 409. The assistant handed to the fake profile answers; taken back,
// the brain says nothing more, and the second writes as it again. The chat
// is renamed in the first, seen so in the second, and deleted for both.
// Last, the assistant goes back to the fake profile for the rounds after.
async function twoRound() {
  const tag = Date.now().toString(36)
  const other = await browser.newContext({ ignoreHTTPSErrors: true })
  const second = await other.newPage()
  second.setDefaultTimeout(timeout)
  await logIn(second, true)
  await heldBy(second, user, 'you, on another device')
  step(`${user} in the second browser`, 'held on another device: it watches')
  await be(second, assistant)
  await brain(second, assistant, 'this device')
  await heldBy(second, assistant, 'you, here')
  await heldBy(page, assistant, 'you, on another device')
  step(`${assistant} taken in the second`, 'the first sees it held on another device')

  await fresh()
  const opened = page.waitForResponse((r) => r.request().method() === 'POST' && /\/api\/reins\/threads\/[^/]+\/entries$/.test(r.url()))
  await page.getByRole('textbox', { name: 'Message input' }).fill(`two of us ${tag}`)
  await page.keyboard.press('Enter')
  const thread = (await (await opened).json()).thread
  await second.getByText(`two of us ${tag}`).first().click()
  await second.locator('[data-role="assistant"]').filter({ hasText: `two of us ${tag}` }).first().waitFor()
  step(`${assistant}'s browser sees ${user}'s`, 'without a reload')

  const answers = await page.locator('[data-role="assistant"]').count()
  await second.getByRole('textbox', { name: 'Message input' }).fill(`from ${assistant} ${tag}`)
  await second.keyboard.press('Enter')
  await page.locator('[data-role="assistant"]').filter({ hasText: `from ${assistant} ${tag}` }).first().waitFor()
  const own = await second.locator('[data-role="user"]').filter({ hasText: `from ${assistant} ${tag}` }).count()
  await page.waitForTimeout(3000)
  const more = (await page.locator('[data-role="assistant"]').count()) - answers
  step(`${user}'s browser sees ${assistant}'s`, `on the other side; its own side in the second: ${own}; others: ${more - 1}`)
  if (own !== 1 || more !== 1) {
    step('FAIL', `${assistant}'s words not shown as said, or a brain answered too`)
    failed = true
  }

  const avatars = await (await second.request.get(url + '/api/reins/me/subjects')).json()
  const device = await second.evaluate(() => localStorage.getItem('units.device'))
  const refused = await second.request.post(`${url}/api/reins/threads/${thread}/entries`, {
    headers: { 'X-Device-Id': device },
    data: { as: avatars.find((a) => a.nick === user)?.id, content: [{ type: 'text', text: 'not mine to say' }] },
  })
  const why = (await refused.text()).trim()
  step(`the second writes as ${user}`, `${refused.status()} ${why}`)
  if (refused.status() !== 409 || why !== 'held by you on another device') {
    step('FAIL', 'writing as an avatar held elsewhere was not refused')
    failed = true
  }

  await brain(second, assistant, 'fake')
  await heldBy(page, assistant, 'brain fake')
  await heldBy(second, assistant, 'brain fake')
  const answered = await say(`now the brain, please ${tag}`)
  step(`${assistant} handed to fake, answers`, JSON.stringify(answered.slice(0, 40)))
  if (!answered) {
    step('FAIL', 'the brain did not answer')
    failed = true
  }

  await brain(second, assistant, 'this device')
  await heldBy(page, assistant, 'you, on another device')
  const before = await page.locator('[data-role="assistant"]').count()
  await page.getByRole('textbox', { name: 'Message input' }).fill(`anyone there? ${tag}`)
  await page.keyboard.press('Enter')
  await page.waitForTimeout(4000)
  const silent = (await page.locator('[data-role="assistant"]').count()) === before && (await page.getByRole('status', { name: 'Writing' }).count()) === 0
  await second.getByRole('textbox', { name: 'Message input' }).fill(`back as myself ${tag}`)
  await second.keyboard.press('Enter')
  await page.locator('[data-role="assistant"]').filter({ hasText: `back as myself ${tag}` }).first().waitFor()
  step('taken back', silent ? 'the brain said nothing; the second writes as it' : 'the brain still spoke')
  if (!silent) {
    step('FAIL', 'the brain answered after the reins were taken back')
    failed = true
  }
  // The chat's menu (UNIT-66): renamed in the first browser, the label
  // changes in the second without a reload; no archive offered; deleted
  // from the first, it is gone from threads for both.
  const item = (p, text) => p.locator('[data-slot="aui_thread-list-item"]').filter({ hasText: text }).first()
  await item(page, `two of us ${tag}`).hover()
  await item(page, `two of us ${tag}`).getByRole('button', { name: 'More options' }).click()
  const archive = await page.getByRole('menuitem', { name: 'Archive' }).count()
  await page.getByRole('menuitem', { name: 'Rename' }).click()
  const box = page.getByRole('textbox', { name: 'Rename thread' })
  const renamed = page.waitForResponse((r) => r.request().method() === 'PATCH' && r.url().endsWith(`/api/reins/threads/${thread}`))
  await box.fill(`renamed ${tag}`)
  await box.press('Enter')
  const rn = await renamed
  await item(second, `renamed ${tag}`).waitFor()
  step('renamed in the first', `${rn.status()}; the second shows "renamed ${tag}" without a reload; archive offered: ${archive}`)
  if (rn.status() !== 200 || archive !== 0) {
    step('FAIL', 'the rename was refused, or archive is still offered')
    failed = true
  }

  await item(page, `renamed ${tag}`).hover()
  await item(page, `renamed ${tag}`).getByRole('button', { name: 'More options' }).click()
  const deleted = page.waitForResponse((r) => r.request().method() === 'DELETE' && r.url().endsWith(`/api/reins/threads/${thread}`))
  // Its click as the page gets it: Playwright's pointer click on this item
  // waits for it forever, while a person's goes through.
  await page.getByRole('menuitem', { name: 'Delete' }).dispatchEvent('click')
  const del = await deleted
  await item(page, `renamed ${tag}`).waitFor({ state: 'detached' })
  const gone = (await second.request.get(`${url}/api/reins/threads/${thread}/entries`)).status()
  await second.reload()
  await second.getByRole('textbox', { name: 'Message input' }).waitFor()
  const listed = await item(second, `renamed ${tag}`).count()
  step('deleted in the first', `${del.status()}; gone from its list; the second reads it: ${gone}, lists it after a reload: ${listed}`)
  if (del.status() !== 204 || gone !== 404 || listed !== 0) {
    step('FAIL', 'the chat was not deleted for both')
    failed = true
  }

  if (process.env.E2E_SHOT) await second.screenshot({ path: process.env.E2E_SHOT.replace(/(\.png)?$/, '-second.png') })
  // Back to the fake profile for the rounds after, from the second.
  await brain(second, assistant, 'fake')
  await heldBy(page, assistant, 'brain fake')
  await other.close()

  // Leaving (UNIT-73): the ghost picked again gives back the avatar held
  // here, to nobody; an avatar held by a brain, stepped into and left, stays
  // the brain's.
  await be(page, 'Ghost')
  await heldBy(page, user, 'nobody')
  const ghostComposer = await page.getByRole('textbox', { name: 'Message input' }).count()
  await be(page, assistant)
  const watching = (await page.getByRole('status', { name: 'Watching' }).innerText()).replace(/\s+/g, ' ')
  await be(page, 'Ghost')
  await page.waitForTimeout(1500)
  const kept = await picker(page).getByRole('radio', { name: new RegExp(`^${assistant}: `) }).getAttribute('aria-label')
  const holds = await (await page.request.get(url + '/api/reins/me/subjects')).json()
  const holder = (nick) => holds.find((a) => a.nick === nick)?.current_brain?.kind ?? 'offline'
  step('the ghost again', `${user}: ${holder(user)}; composer ${ghostComposer}; in ${assistant}: ${JSON.stringify(watching)}; left: ${kept}`)
  if (holder(user) !== 'offline' || ghostComposer || !watching.includes('brain fake') || kept !== `${assistant}: brain fake` || holder(assistant) !== 'brain') {
    step('FAIL', 'leaving an avatar did not give back only what this device held')
    failed = true
  }
}

// stopRound: a reply being written is stopped from the composer's row, an
// action on the brain through reins. What it wrote so far is kept as said;
// stopped before it wrote anything, nothing is kept. Fake model only.
async function stopRound() {
  await fresh()
  const box = page.getByRole('textbox', { name: 'Message input' })
  await box.fill('Count for me. #slow')
  await page.keyboard.press('Enter')
  await page.locator('[data-role="assistant"]').filter({ hasText: 'Counting slowly: one' }).first().waitFor()
  await page.getByRole('button', { name: /^Stop / }).click()
  await page.getByRole('status', { name: 'Writing' }).waitFor({ state: 'detached' })
  await box.fill('Take your time. #late')
  await page.keyboard.press('Enter')
  await page.getByRole('button', { name: /^Stop / }).click()
  await page.getByRole('status', { name: 'Writing' }).waitFor({ state: 'detached' })
  await page.waitForTimeout(1500)
  await page.reload()
  await page.getByText('Take your time. #late').first().waitFor()
  const kept = await page.locator('[data-role="assistant"]').allInnerTexts()
  step('stopped, after a reload', JSON.stringify(kept.map((t) => t.trim().slice(0, 40))))
  if (kept.length !== 1 || !kept[0].includes('Counting slowly') || kept[0].includes('ten.')) {
    step('FAIL', 'a stopped reply was not kept as far as it went, or an empty one was kept')
    failed = true
  }
}

// meRound: who is logged in, at the sidebar's foot, from gate's userinfo;
// its log-out ends the session and lands on the login page. Last, since
// it logs out. E2E_SHOT names a file for a picture of the page before.
async function meRound() {
  const foot = page.getByRole('contentinfo', { name: 'Who is logged in' }).or(page.getByLabel('Who is logged in'))
  await foot.first().waitFor()
  const shown = (await foot.first().innerText()).replace(/\s+/g, ' ').trim()
  step('the sidebar foot', JSON.stringify(shown))
  if (process.env.E2E_SHOT) await page.screenshot({ path: process.env.E2E_SHOT })
  if (!shown.includes(user)) {
    step('FAIL', `${user} is not shown`)
    failed = true
  }
  await page.getByRole('button', { name: 'Log out' }).click()
  await page.locator('#username-textfield').waitFor()
  step('log out: the login page', new URL(page.url()).pathname)
  const after = await page.request.get(url + '/api/v1/userinfo')
  step('userinfo after', after.status())
  if (after.status() !== 401) {
    step('FAIL', 'the session outlived log out')
    failed = true
  }
}

// choose picks name in the picker called label and returns what it offered.
async function choose(label, name) {
  await page.getByRole('combobox', { name: new RegExp(`^${label}:`) }).click()
  const offered = await page.getByRole('option').allInnerTexts()
  await page.getByRole('option', { name, exact: true }).click()
  await page.getByRole('option').first().waitFor({ state: 'detached' })
  return offered
}

// settings opens the settings from the sidebar's foot, goes to nick's
// avatar, fills its text fields and picks its preferences (label → what),
// saves when anything changed, and closes. It returns what each picker
// showed before and offered, and the text of the fields named in read.
async function settings(nick, picks = {}, text = {}, read = []) {
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Settings' })
  await dialog.waitFor()
  await dialog.getByRole('button', { name: nick, exact: true }).click()
  const form = dialog.getByRole('form', { name: `Avatar ${nick}` })
  await form.waitFor()
  const got = { was: {}, offered: {}, read: {} }
  for (const label of read) got.read[label] = await form.getByLabel(label, { exact: true }).inputValue()
  for (const [label, value] of Object.entries(text)) await form.getByLabel(label, { exact: true }).fill(value)
  for (const [label, name] of Object.entries(picks)) {
    got.was[label] = await form.getByRole('combobox', { name: new RegExp(`^${label}:`) }).getAttribute('aria-label')
    got.offered[label] = await choose(label, name)
  }
  const save = form.getByRole('button', { name: 'Save' })
  if (await save.isEnabled()) {
    await save.click()
    await form.getByRole('status').filter({ hasText: 'Saved' }).waitFor()
  }
  await page.keyboard.press('Escape')
  await dialog.waitFor({ state: 'detached' })
  return got
}

// soundPickRound: the person's ear and the assistant's mouth set to the
// second fake ones in the settings, and a voice of the mouth's own: what
// comes back is theirs.
async function soundPickRound() {
  await page.reload()
  await page.getByRole('textbox', { name: 'Message input' }).waitFor()
  const ears = (await settings(user, { 'Speech to text': 'fake-stt-b' })).offered['Speech to text']
  step('ears offered', ears.join(' '))
  const box = page.getByRole('textbox', { name: 'Message input' })
  await page.getByRole('button', { name: 'Start voice input' }).click()
  await page.getByRole('button', { name: 'Stop voice input' }).waitFor()
  await page.waitForTimeout(1000)
  await page.getByRole('button', { name: 'Stop voice input' }).click()
  await page.getByRole('button', { name: 'Start voice input' }).waitFor()
  const heard = await box.inputValue()
  step('the second ear heard', JSON.stringify(heard))

  const set = await settings(assistant, { 'Text to speech': 'fake-tts-b', Voice: 'irina' })
  const mouths = set.offered['Text to speech']
  const voices = set.offered.Voice
  step('mouths, its voices', `${mouths.join(' ')}; ${voices.join(' ')}`)
  await say(heard || 'Say something.')
  const answer = page.locator('[data-role="assistant"]').last()
  await answer.hover()
  const spoken = page.waitForResponse((r) => r.url().endsWith('/api/v1/audio/speech'))
  await answer.getByRole('button', { name: 'Read aloud' }).click()
  const sound = await spoken
  const asked = JSON.parse(sound.request().postData() ?? '{}')
  const bytes = (await sound.body()).length
  step('read aloud by the second mouth', `${sound.status()} ${bytes} bytes, asked ${asked.model} ${asked.voice}`)
  if (heard !== 'What the second fake ear heard.' || !ears.includes('fake-stt') || !mouths.includes('fake-tts')
    || voices.join() !== 'denis,irina' || sound.status() !== 200 || bytes !== 3244 || asked.model !== 'fake-tts-b' || asked.voice !== 'irina') {
    step('FAIL', 'the picked ear, mouth or voice did not answer')
    failed = true
  }
}

// refusedRound: a picture to a model that takes none. The run goes on: the
// mind puts a placeholder in words where the picture stood (UNIT-97), and
// the fake model, told of a picture not shown, says so.
async function refusedRound() {
  await handOther('fake-text')
  await fresh()
  await attach(dot)
  const said = await say('What is in the picture?')
  step('a picture to a text-only model', JSON.stringify(said.slice(-60)))
  if (!said.includes('told of a picture, not shown it')) {
    step('FAIL', 'the model was not told of the picture in words')
    failed = true
  }
  await handOther('fake')
}

// drawRound: the assistant draws with a tool (#draw: the fake model calls
// images__draw, tools/images asks LiteLLM's fake-images for the picture).
// What the tool returned is the tool's: the page shows a tool card inside
// the assistant's message — which tool, the prompt, the picture captioned
// as the tool's — and the picture is not among the assistant's words. A
// reload keeps it: it was journaled. E2E_SHOT names a file for a picture
// of the card.
async function drawRound() {
  await fresh()
  const said = await say('Draw me a red circle, please. #draw')
  step('asked to draw', JSON.stringify(said.slice(-60)))
  const look = async (when) => {
    const message = page.locator('[data-role="assistant"]').last()
    const card = message.locator('[data-slot="tool-card"]')
    await card.waitFor()
    const label = await card.getAttribute('aria-label')
    const img = card.locator('img')
    await img.waitFor()
    const src = (await img.getAttribute('src')) ?? ''
    const drawn = await img.evaluate((i) => i.complete && i.naturalWidth > 0)
    const caption = await card.locator('figcaption').innerText()
    const prompt = await card.getByText('a red circle on white').count()
    const outside = await message.locator('img').count()
    step(`${when}: the card`, `${label}; ${src.slice(0, 22)}…, ${drawn ? 'drawn' : 'not drawn'}, "${caption}"`)
    if (label !== 'Tool images · draw' || !src.startsWith('data:image/png;base64,') || !drawn || caption !== 'from images · draw' || !prompt) {
      step('FAIL', `${when}: no tool card with the picture and the prompt, captioned as the tool's`)
      failed = true
    }
    if (outside !== 1) {
      step('FAIL', `${when}: ${outside} pictures in the message, the card's alone expected`)
      failed = true
    }
    // The assistant's words are beside the card, not on it.
    const all = await message.innerText()
    const onCard = await card.innerText()
    if (!all.includes('The tool said') || onCard.includes('The tool said')) {
      step('FAIL', `${when}: the assistant's words are not beside the card: ${JSON.stringify(all.slice(-120))}`)
      failed = true
    }
    return card
  }
  const card = await look('as said')
  if (process.env.E2E_SHOT) await card.screenshot({ path: process.env.E2E_SHOT.replace(/(\.png)?$/, '-card.png') })
  if (process.env.E2E_SHOT) await page.screenshot({ path: process.env.E2E_SHOT })
  await page.reload()
  await page.getByText('Draw me a red circle, please. #draw').first().waitFor()
  await look('after a reload')
}

// handOther hands the assistant to a profile: stepped into (leaving the
// person's own avatar, given back), handed from its radio, then back in
// the person's own, taken here again.
async function handOther(profile) {
  await be(page, assistant)
  await brain(page, assistant, profile)
  await heldBy(page, assistant, `brain ${profile}`)
  await be(page, user)
  await brain(page, user, 'this device')
  await page.getByRole('textbox', { name: 'Message input' }).waitFor()
}

// fullRound: the conversation, tools, reasoning, attachments.
async function fullRound() {
  await fresh()
  // The answer streams: while the driver writes it, the page shows it
  // growing and says who is writing, before the entry is said.
  const writing = page.getByRole('status', { name: 'Writing' }).waitFor().then(() => page.getByRole('status', { name: 'Writing' }).innerText(), () => '')
  const a1 = await say(first)
  const seen = await writing
  step('an answer', JSON.stringify(a1.slice(0, 40)))
  step('while written', JSON.stringify(seen))
  if (!seen.includes('is writing')) {
    step('FAIL', 'the answer did not stream: nobody was seen writing')
    failed = true
  }
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

  // Reasoning is shown while the reply is written, and is not journaled:
  // the reply said is the answer alone.
  const a4 = await say(think)
  step('thinks, then answers', JSON.stringify(a4.slice(-60)))
  if (!a4 || (fake && (!a4.includes('Thought it over.') || a4.includes('So I think')))) {
    step('FAIL', `reasoning and answer not apart: ${JSON.stringify(a4.slice(-80))}`)
    failed = true
  }

  // Within one run the mind gives the signatures back with the tool call.
  // A run later they are gone: the opaque part is not what was said, so
  // the journal does not keep it (#signatures_back is not asked).
  const a5 = await say(signed)
  step('a sum, thought out, by a tool', JSON.stringify(a5.slice(-60)))
  if (fake && !a5.includes('its signature came back')) {
    step('FAIL', 'the thought signatures did not go back with the tool call')
    failed = true
  }

  // Over the limit: the page says why when the person points at the file,
  // and pressing send sends nothing while it is there.
  let runs = 0
  page.on('request', (r) => { if (r.method() === 'POST' && /\/api\/reins\/(threads\/[^/]+\/entries|me\/threads)$/.test(r.url())) runs++ })
  await attach(huge)
  await page.getByLabel('Document attachment huge.txt, upload failed').hover({ position: { x: 12, y: 44 } })
  const refused = page.getByText(/Too large: 11\.0 MB/).first()
  await refused.waitFor()
  step('a file over the limit', await refused.innerText())
  const sent = runs
  await page.getByRole('textbox', { name: 'Message input' }).fill('this goes nowhere')
  await page.keyboard.press('Enter')
  await page.waitForTimeout(2000)
  step('sent with it', `${runs - sent} entries`)
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

  if (fake) await failedRound()
}

// The scenarios, each a run of its own by name (E2E_ONLY=<name>): a login,
// then its round. `needs` is what a scenario asks of the lab, `has` whether
// this one has it; without it the scenario is skipped, and says so. Without
// E2E_ONLY, the whole round in one browser: `greet` (with E2E_FIRST_LOGIN)
// and then the scenarios in `whole`, in order.
const scenarios = {
  greet: { round: greetRound },
  conversation: { round: fullRound },
  draw: { round: drawRound, needs: 'the fake model', has: fake },
  stop: { round: stopRound, needs: 'the fake model', has: fake },
  model: { round: modelRound, needs: 'the fake model', has: fake },
  threads: { round: threadsRound },
  voice: { round: voiceRound },
  sound: { round: soundPickRound, needs: 'the fake ear', has: fakeEar },
  call: { round: callRound, needs: 'a realtime model', has: !!realtime },
  me: { round: meRound },
  two: { round: twoRound },
  refused: { round: refusedRound },
  failed: { round: failedRound, needs: 'the fake model', has: fake },
}
const whole = ['conversation', 'draw', 'stop', 'model', 'threads', 'voice', 'sound', 'call', 'me']
const runs = (name) => !scenarios[name].needs || scenarios[name].has
if (only && !scenarios[only]) {
  console.log(`  FAIL no scenario ${JSON.stringify(only)}; there are ${Object.keys(scenarios).join(', ')}`)
  await browser.close()
  process.exit(2)
}
if (only && !runs(only)) {
  console.log(`  scenario ${only}: skipped, it needs ${scenarios[only].needs}`)
  await browser.close()
  process.exit(0)
}

try {
  if (only) console.log(`  scenario ${only}`)
  await page.goto(url + '/')
  step('no session: the login page', new URL(page.url()).pathname)
  await logIn(page)
  step(`${user} logs in, the chat`, await page.title())

  // A scenario alone starts where the first login leaves the lab: the
  // assistant held by the fake profile (one before may have left it to
  // nobody: a brain that failed lets go). greet looks at that state itself.
  if (only && only !== 'greet') {
    const held = await pickerItem(page, assistant).getAttribute('aria-label')
    if (held !== `${assistant}: brain fake`) {
      step('the assistant, first', `${held}; handed to fake`)
      await handOther('fake')
    }
  }
  if (only) await scenarios[only].round()
  else {
    if (process.env.E2E_FIRST_LOGIN) await greetRound()
    for (const name of whole) if (runs(name)) await scenarios[name].round()
  }
} catch (e) {
  console.log(`  FAIL ${e.message.split('\n')[0]}`)
  await page.screenshot({ path: '/tmp/e2e-fail.png' }).catch(() => {})
  // What reins holds now, read afresh beside what the page showed: a step
  // the page missed is told from one that never happened.
  try {
    const threads = await (await page.request.get(url + '/api/reins/me/threads')).json()
    const newest = threads[0]
    const entries = newest ? await (await page.request.get(`${url}/api/reins/threads/${newest.id}/entries`)).json() : []
    console.log(`  reins now: ${threads.length} threads; the newest's entries ${JSON.stringify(entries.map((x) => `${x.seq} ${x.kind}`))}`)
    console.log(`  the page: ${await page.locator('[data-slot="aui_thread-list-item"]').count()} threads listed, ${await page.locator('[data-role="assistant"]').count()} on the left, at ${new URL(page.url()).pathname}`)
  } catch {}
  failed = true
} finally {
  await browser.close()
}
process.exit(failed ? 1 : 0)
