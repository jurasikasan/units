import {
  CompositeAttachmentAdapter,
  SimpleImageAttachmentAdapter,
  SimpleTextAttachmentAdapter,
  type AttachmentAdapter,
  type PendingAttachment,
} from '@assistant-ui/react'

// What a person can attach, and how it leaves: assistant-ui's own adapters
// for pictures (a data URL) and text files (the text, naming the file), and
// a PDF as a file part. The AG-UI runtime sends them as InputContent parts
// (image, document) with the message; the whole conversation goes with each
// run, so a file goes inline every time.

// MAX_FILE is the largest file taken. The mind takes runs of 16 MB, and
// base64 makes a file a third larger.
export const MAX_FILE = 10 * 1024 * 1024

const readDataURL = (file: File) =>
  new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as string)
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })

// A PDF goes as it is, for the model to read: a file part, not text.
class PdfAttachmentAdapter implements AttachmentAdapter {
  accept = 'application/pdf'

  async add({ file }: { file: File }): Promise<PendingAttachment> {
    return {
      id: crypto.randomUUID(),
      type: 'document',
      name: file.name,
      contentType: file.type,
      file,
      status: { type: 'requires-action', reason: 'composer-send' },
    }
  }

  async send(attachment: PendingAttachment) {
    return {
      ...attachment,
      status: { type: 'complete' as const },
      content: [
        {
          type: 'file' as const,
          data: await readDataURL(attachment.file),
          mimeType: 'application/pdf',
          filename: attachment.name,
        },
      ],
    }
  }

  async remove() {}
}

// limited refuses a file over MAX_FILE when it is attached: the person sees
// why on the attachment, and it never goes out.
function limited(adapter: AttachmentAdapter): AttachmentAdapter {
  return {
    accept: adapter.accept,
    add(state) {
      const { file } = state
      if (file.size <= MAX_FILE) return adapter.add(state)
      return Promise.resolve({
        id: crypto.randomUUID(),
        type: file.type.startsWith('image/') ? 'image' : 'document',
        name: file.name,
        contentType: file.type,
        file,
        status: {
          type: 'incomplete',
          reason: 'error',
          message: `Too large: ${(file.size / 1024 / 1024).toFixed(1)} MB, at most ${MAX_FILE / 1024 / 1024} MB`,
        },
      })
    },
    send(attachment, options) {
      if (attachment.file.size > MAX_FILE) {
        return Promise.reject(new Error(`${attachment.name} is over ${MAX_FILE / 1024 / 1024} MB`))
      }
      return adapter.send(attachment, options)
    },
    remove: (attachment) => adapter.remove(attachment),
  }
}

export const attachments = new CompositeAttachmentAdapter([
  limited(new SimpleImageAttachmentAdapter()),
  limited(new SimpleTextAttachmentAdapter()),
  limited(new PdfAttachmentAdapter()),
])
