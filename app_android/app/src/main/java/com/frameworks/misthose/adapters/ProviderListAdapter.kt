package com.frameworks.misthose.adapters

import android.content.Context
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.BaseAdapter
import android.widget.TextView
import com.frameworks.misthose.R
import com.frameworks.misthose.models.StreamProvider

class ProviderListAdapter(
    private val context: Context,
    private val providers: List<StreamProvider>,
    private val onProviderSelected: (StreamProvider) -> Unit
) : BaseAdapter() {
    
    override fun getCount(): Int = providers.size
    
    override fun getItem(position: Int): StreamProvider = providers[position]
    
    override fun getItemId(position: Int): Long = position.toLong()
    
    override fun getView(position: Int, convertView: View?, parent: ViewGroup?): View {
        val view = convertView ?: LayoutInflater.from(context)
            .inflate(android.R.layout.simple_list_item_2, parent, false)
        
        val provider = providers[position]
        
        val titleView = view.findViewById<TextView>(android.R.id.text1)
        val subtitleView = view.findViewById<TextView>(android.R.id.text2)
        
        titleView.text = provider.name
        subtitleView.text = when (provider.type) {
            com.frameworks.misthose.models.ProviderType.STATIC -> {
                val config = provider.staticConfig
                when (config?.protocol) {
                    com.frameworks.misthose.models.StreamProtocol.SRT -> "SRT - ${config.serverUrl}:${config.port}"
                    com.frameworks.misthose.models.StreamProtocol.WHIP -> "WHIP - ${config.serverUrl}"
                    else -> "${config?.protocol?.displayName} - ${config?.serverUrl}"
                }
            }
            com.frameworks.misthose.models.ProviderType.FRAMEWORKS -> "FrameWorks Service"
            com.frameworks.misthose.models.ProviderType.CUSTOM_SERVICE -> {
                "Custom Service - ${provider.serviceConfig?.baseUrl}"
            }
        }
        
        view.setOnClickListener {
            onProviderSelected(provider)
        }
        
        return view
    }
} 