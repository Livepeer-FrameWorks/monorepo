const express = require('express');
const cors = require('cors');
const helmet = require('helmet');
const rateLimit = require('express-rate-limit');
const nodemailer = require('nodemailer');
require('dotenv').config();

const app = express();
const PORT = process.env.PORT || 18032;

// Security middleware
app.use(helmet());
app.use(cors({
  origin: function (origin, callback) {
    // Allow requests with no origin (like mobile apps or curl requests)
    if (!origin) return callback(null, true);

    // Allow all localhost origins
    if (origin.startsWith('http://localhost') || origin.startsWith('https://localhost')) {
      return callback(null, true);
    }

    // Allow configured origins
    const allowedOrigins = process.env.ALLOWED_ORIGINS?.split(',') || ['https://frameworks.network'];
    if (allowedOrigins.includes(origin)) {
      return callback(null, true);
    }

    // Reject all other origins
    callback(new Error('Not allowed by CORS'));
  },
  credentials: true
}));
app.use(express.json({ limit: '10mb' }));

// Rate limiting
const contactLimiter = rateLimit({
  windowMs: 15 * 60 * 1000, // 15 minutes
  max: 5, // limit each IP to 5 requests per windowMs
  message: { error: 'Too many contact submissions, please try again later.' },
  standardHeaders: true,
  legacyHeaders: false,
});

// Email transporter setup
const createTransporter = () => {
  if (process.env.SMTP_HOST) {
    return nodemailer.createTransport({
      host: process.env.SMTP_HOST,
      port: process.env.SMTP_PORT || 587,
      secure: false,
      auth: {
        user: process.env.SMTP_USER,
        pass: process.env.SMTP_PASS
      }
    });
  } else {
    console.warn('No SMTP configuration found, emails will be logged to console');
    return null;
  }
};

const transporter = createTransporter();

// Level 3 Anti-Spam Validation
const validateSubmission = (req) => {
  const { name, email, company, message, phone_number, human_check, behavior } = req.body;
  const errors = [];

  // 1. 🕳️ Honeypot Check
  if (phone_number && phone_number.trim() !== '') {
    errors.push('Honeypot field filled (bot detected)');
  }

  // 2. 🔘 Human Check Toggle
  if (human_check !== 'human') {
    errors.push('Human verification not selected');
  }

  // 3. 🖱️ Behavioral Analysis
  if (behavior) {
    const behaviorData = typeof behavior === 'string' ? JSON.parse(behavior) : behavior;

    // Check if form was filled too quickly (less than 3 seconds)
    const timeSpent = behaviorData.submittedAt - behaviorData.formShownAt;
    if (timeSpent < 3000) {
      errors.push('Form submitted too quickly');
    }

    // Check for human interactions
    if (!behaviorData.mouse && !behaviorData.typed) {
      errors.push('No human interaction detected');
    }

    // Check if form was open for too long (likely abandoned/bot)
    if (timeSpent > 30 * 60 * 1000) { // 30 minutes
      errors.push('Form session expired');
    }
  } else {
    errors.push('Missing behavioral data');
  }

  // 4. Basic field validation
  if (!name || name.trim().length < 2) {
    errors.push('Name is required (minimum 2 characters)');
  }

  if (!email || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
    errors.push('Valid email is required');
  }

  if (!message || message.trim().length < 10) {
    errors.push('Message is required (minimum 10 characters)');
  }

  // 5. Spam keyword detection
  const spamKeywords = ['crypto', 'bitcoin', 'investment', 'loan', 'casino', 'viagra', 'pharmacy'];
  const content = `${name} ${email} ${company} ${message}`.toLowerCase();
  const foundSpam = spamKeywords.filter(keyword => content.includes(keyword));
  if (foundSpam.length > 0) {
    errors.push(`Potential spam keywords detected: ${foundSpam.join(', ')}`);
  }

  return errors;
};

// Contact form endpoint
app.post('/api/contact', contactLimiter, async (req, res) => {
  try {
    // Validate submission
    const validationErrors = validateSubmission(req);

    if (validationErrors.length > 0) {
      console.log('Blocked submission:', {
        ip: req.ip,
        errors: validationErrors,
        body: req.body
      });

      return res.status(400).json({
        success: false,
        error: 'Submission failed validation',
        details: process.env.NODE_ENV === 'development' ? validationErrors : undefined
      });
    }

    const { name, email, company, message } = req.body;

    // Prepare email content
    const emailContent = {
      from: process.env.FROM_EMAIL || 'noreply@frameworks.network',
      to: process.env.TO_EMAIL || 'contact@frameworks.network',
      subject: `FrameWorks Contact Form: ${name}`,
      html: `
        <h2>New Contact Form Submission</h2>
        <p><strong>Name:</strong> ${name}</p>
        <p><strong>Email:</strong> ${email}</p>
        <p><strong>Company:</strong> ${company || 'Not provided'}</p>
        <p><strong>Message:</strong></p>
        <div style="background: #f5f5f5; padding: 15px; border-radius: 5px; margin: 10px 0;">
          ${message.replace(/\n/g, '<br>')}
        </div>
        <hr>
        <p><small>Submitted at: ${new Date().toISOString()}</small></p>
        <p><small>IP: ${req.ip}</small></p>
      `
    };

    // Send email
    if (transporter) {
      await transporter.sendMail(emailContent);
      console.log('Email sent successfully:', { name, email, company });
    } else {
      console.log('EMAIL CONTENT (no SMTP configured):', emailContent);
    }

    res.json({
      success: true,
      message: 'Thank you for your message! We\'ll get back to you soon.'
    });

  } catch (error) {
    console.error('Contact form error:', error);
    res.status(500).json({
      success: false,
      error: 'Internal server error'
    });
  }
});

// Health check endpoint
app.get('/health', (req, res) => {
  res.json({ status: 'ok', timestamp: new Date().toISOString() });
});

app.listen(PORT, () => {
  console.log(`FrameWorks Contact API running on port ${PORT}`);
  console.log(`SMTP configured: ${!!transporter}`);
}); 