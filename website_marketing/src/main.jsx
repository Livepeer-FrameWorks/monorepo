import { StrictMode } from 'react'
import ReactDOM from 'react-dom/client'
import '@livepeer-frameworks/player-core/player.css'
import App from './App.jsx'
import './index.css'

ReactDOM.createRoot(document.getElementById('root')).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
